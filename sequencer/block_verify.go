package sequencer

import (
	"fmt"
	"math/big"

	"github.com/morph-l2/go-ethereum/common"
	"github.com/morph-l2/go-ethereum/core/types"
	"github.com/morph-l2/go-ethereum/crypto"
	"github.com/morph-l2/go-ethereum/trie"
)

// computeBlockHash recomputes a BlockV2's block hash from its content fields.
//
// This MUST stay in lockstep with geth's catalyst.executableDataToBlock so that
// honest blocks always pass: the sequencer signs newBlockResult.Block.Hash(),
// which is computed from the same canonical header layout used here.
//
// Notes on field alignment:
//   - Difficulty=0, Nonce=0, UncleHash=EmptyUncleHash, Extra=[] are L2 consensus
//     constants (consensus/l2 sets them in Prepare and executableDataToBlock).
//   - NextL1MsgIndex and BatchHash live in the Header struct but are NOT included
//     in Header.Hash() (see go-ethereum/core/types/block.go headerHashing layout),
//     so we deliberately do not need to set them for hash computation.
//   - Coinbase is taken directly from block.Miner. Honest sequencers always send
//     Miner = newBlockResult.Block.Coinbase(), which is empty for V2 blocks
//     (BuildBlock does not set coinbase, and the L2 engine's Prepare overrides
//     to EmptyAddress when FeeVault is enabled). We do not re-apply the FeeVault
//     override here: it is unnecessary in the honest case, and rejecting any
//     mismatch is the correct behavior in the attacker case.
func computeBlockHash(block *BlockV2) (common.Hash, error) {
	txs := make(types.Transactions, len(block.Transactions))
	for i, raw := range block.Transactions {
		var tx types.Transaction
		if err := tx.UnmarshalBinary(raw); err != nil {
			return common.Hash{}, fmt.Errorf("decode tx %d: %w", i, err)
		}
		txs[i] = &tx
	}

	header := &types.Header{
		ParentHash:  block.ParentHash,
		UncleHash:   types.EmptyUncleHash,
		Coinbase:    block.Miner,
		Root:        block.StateRoot,
		TxHash:      types.DeriveSha(txs, trie.NewStackTrie(nil)),
		ReceiptHash: block.ReceiptRoot,
		Bloom:       types.BytesToBloom(block.LogsBloom),
		Difficulty:  new(big.Int),
		Number:      new(big.Int).SetUint64(block.Number),
		GasLimit:    block.GasLimit,
		GasUsed:     block.GasUsed,
		Time:        block.Timestamp,
		Extra:       []byte{},
		BaseFee:     block.BaseFee,
	}
	return header.Hash(), nil
}

// VerifyBlockSignature verifies a block's ECDSA signature against the
// expected sequencer at that block's height. All V2 blocks must carry a valid signature.
//
// Hash binding: before recovering the signer, this function recomputes the
// block hash from the canonical content fields and rejects the block if it does
// not match block.Hash. Without this binding, an attacker could pair a valid
// (Hash, Signature) recovered from a legitimately signed block with a tampered
// body, and other peers would accept it - causing a chain fork at the next
// height. The geth-side check in NewL2BlockV2 is the safety net; this check
// rejects tampered blocks early so we never persist orphan signatures into
// SignatureStore.
func VerifyBlockSignature(verifier SequencerVerifier, block *BlockV2) error {
	if verifier == nil {
		return fmt.Errorf("%w: verifier not configured", ErrInvalidSignature)
	}

	if len(block.Signature) == 0 {
		return fmt.Errorf("%w: missing signature at height %d", ErrInvalidSignature, block.Number)
	}

	computedHash, err := computeBlockHash(block)
	if err != nil {
		return fmt.Errorf("%w: compute hash at height %d: %v", ErrInvalidSignature, block.Number, err)
	}
	if block.Hash != computedHash {
		return fmt.Errorf("%w: hash mismatch at height %d: declared %s, computed %s",
			ErrInvalidSignature, block.Number, block.Hash.Hex(), computedHash.Hex())
	}

	pubKey, err := crypto.SigToPub(computedHash.Bytes(), block.Signature)
	if err != nil {
		return fmt.Errorf("%w: recover pubkey at height %d: %v", ErrInvalidSignature, block.Number, err)
	}
	signer := crypto.PubkeyToAddress(*pubKey)

	ok, err := verifier.IsSequencerAt(signer, block.Number)
	if err != nil {
		return fmt.Errorf("IsSequencerAt height %d: %w", block.Number, err)
	}
	if !ok {
		return fmt.Errorf("%w: signer %s is not sequencer at height %d",
			ErrInvalidSignature, signer.Hex(), block.Number)
	}
	return nil
}
