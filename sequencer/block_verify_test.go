package sequencer

import (
	"errors"
	"math/big"
	"testing"

	"github.com/morph-l2/go-ethereum/common"
	"github.com/morph-l2/go-ethereum/core/types"
	"github.com/morph-l2/go-ethereum/crypto"

	tmtypes "github.com/tendermint/tendermint/types"
)

// makeTestBlock builds a BlockV2 whose Hash is computed via computeBlockHash and
// whose Signature is produced by the given private key. The block is what an
// honest sequencer would produce.
func makeTestBlock(t *testing.T, priv []byte) (*tmtypes.BlockV2, common.Address) {
	t.Helper()

	key, err := crypto.ToECDSA(priv)
	if err != nil {
		t.Fatalf("ToECDSA: %v", err)
	}
	signer := crypto.PubkeyToAddress(key.PublicKey)

	tx := types.NewTransaction(0, common.HexToAddress("0xdead"), big.NewInt(1), 21000, big.NewInt(1), nil)
	rawTx, err := tx.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}

	block := &tmtypes.BlockV2{
		ParentHash:         common.HexToHash("0xabc"),
		Miner:              common.Address{},
		Number:             42,
		GasLimit:           30_000_000,
		BaseFee:            big.NewInt(1_000_000_000),
		Timestamp:          1_700_000_000,
		Transactions:       [][]byte{rawTx},
		StateRoot:          common.HexToHash("0xdef"),
		GasUsed:            21_000,
		ReceiptRoot:        common.HexToHash("0x123"),
		LogsBloom:          make([]byte, 256),
		WithdrawTrieRoot:   common.HexToHash("0x456"),
		NextL1MessageIndex: 7,
	}

	h, err := computeBlockHash(block)
	if err != nil {
		t.Fatalf("computeBlockHash: %v", err)
	}
	block.Hash = h

	sig, err := crypto.Sign(h.Bytes(), key)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	block.Signature = sig

	return block, signer
}

// testKey is a deterministic private key for tests; do NOT use elsewhere.
var testKey = []byte{
	0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88,
	0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00,
	0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88,
	0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00,
}

func TestVerifyBlockSignature_Honest(t *testing.T) {
	block, _ := makeTestBlock(t, testKey)
	verifier := &mockSequencerVerifier{isSequencer: true}

	if err := VerifyBlockSignature(verifier, block); err != nil {
		t.Fatalf("expected honest block to verify, got: %v", err)
	}
}

func TestVerifyBlockSignature_TamperedMiner(t *testing.T) {
	block, _ := makeTestBlock(t, testKey)
	verifier := &mockSequencerVerifier{isSequencer: true}

	// Attacker keeps the original Hash + Signature but forges the Miner field.
	block.Miner = common.HexToAddress("0xbadbadbadbadbadbadbadbadbadbadbadbadbad0")

	err := VerifyBlockSignature(verifier, block)
	if err == nil {
		t.Fatal("expected error for tampered Miner, got nil")
	}
	if !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("expected ErrInvalidSignature, got: %v", err)
	}
}

func TestVerifyBlockSignature_TamperedStateRoot(t *testing.T) {
	block, _ := makeTestBlock(t, testKey)
	verifier := &mockSequencerVerifier{isSequencer: true}

	block.StateRoot = common.HexToHash("0xdeadbeef")

	err := VerifyBlockSignature(verifier, block)
	if err == nil {
		t.Fatal("expected error for tampered StateRoot, got nil")
	}
	if !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("expected ErrInvalidSignature, got: %v", err)
	}
}

func TestVerifyBlockSignature_TamperedTransactions(t *testing.T) {
	block, _ := makeTestBlock(t, testKey)
	verifier := &mockSequencerVerifier{isSequencer: true}

	tx := types.NewTransaction(99, common.HexToAddress("0xfeed"), big.NewInt(999), 21000, big.NewInt(1), nil)
	raw, err := tx.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	block.Transactions = append(block.Transactions, raw)

	if verr := VerifyBlockSignature(verifier, block); verr == nil {
		t.Fatal("expected error for appended transaction, got nil")
	}
}

func TestVerifyBlockSignature_NotSequencer(t *testing.T) {
	block, _ := makeTestBlock(t, testKey)
	verifier := &mockSequencerVerifier{isSequencer: false}

	err := VerifyBlockSignature(verifier, block)
	if err == nil {
		t.Fatal("expected error when signer is not the active sequencer, got nil")
	}
	if !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("expected ErrInvalidSignature, got: %v", err)
	}
}

func TestVerifyBlockSignature_MissingSignature(t *testing.T) {
	block, _ := makeTestBlock(t, testKey)
	block.Signature = nil
	verifier := &mockSequencerVerifier{isSequencer: true}

	err := VerifyBlockSignature(verifier, block)
	if err == nil {
		t.Fatal("expected error for missing signature, got nil")
	}
	if !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("expected ErrInvalidSignature, got: %v", err)
	}
}

func TestVerifyBlockSignature_NilVerifier(t *testing.T) {
	block, _ := makeTestBlock(t, testKey)

	err := VerifyBlockSignature(nil, block)
	if err == nil {
		t.Fatal("expected error for nil verifier, got nil")
	}
	if !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("expected ErrInvalidSignature, got: %v", err)
	}
}

func TestComputeBlockHash_DeterministicAndDistinct(t *testing.T) {
	b1, _ := makeTestBlock(t, testKey)
	h1, err := computeBlockHash(b1)
	if err != nil {
		t.Fatalf("computeBlockHash: %v", err)
	}

	// Same content produces same hash.
	h1again, err := computeBlockHash(b1)
	if err != nil {
		t.Fatalf("computeBlockHash: %v", err)
	}
	if h1 != h1again {
		t.Fatalf("computeBlockHash not deterministic: %s vs %s", h1.Hex(), h1again.Hex())
	}

	// Different content produces different hash.
	b2 := *b1
	b2.GasUsed = b1.GasUsed + 1
	h2, err := computeBlockHash(&b2)
	if err != nil {
		t.Fatalf("computeBlockHash: %v", err)
	}
	if h1 == h2 {
		t.Fatal("computeBlockHash should differ when GasUsed changes")
	}
}
