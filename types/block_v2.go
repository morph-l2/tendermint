package types

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/morph-l2/go-ethereum/common"
	"github.com/morph-l2/go-ethereum/crypto"
	seqproto "github.com/tendermint/tendermint/proto/tendermint/sequencer"
)

// BlockV2 represents the block format after upgrade to centralized sequencer mode.
// It is based on ExecutableL2Data from go-ethereum with an added signature field.
type BlockV2 struct {
	// Block header fields
	ParentHash common.Hash    `json:"parentHash"`
	Miner      common.Address `json:"miner"`
	Number     uint64         `json:"number"`
	GasLimit   uint64         `json:"gasLimit"`
	BaseFee    *big.Int       `json:"baseFeePerGas"`
	Timestamp  uint64         `json:"timestamp"`

	// Transactions
	Transactions [][]byte `json:"transactions"`

	// Execution result
	StateRoot        common.Hash `json:"stateRoot"`
	GasUsed          uint64      `json:"gasUsed"`
	ReceiptRoot      common.Hash `json:"receiptsRoot"`
	LogsBloom        []byte      `json:"logsBloom"`
	WithdrawTrieRoot common.Hash `json:"withdrawTrieRoot"`

	// L1 message index
	NextL1MessageIndex uint64 `json:"nextL1MessageIndex"`

	// Block hash
	Hash common.Hash `json:"hash"`

	// Sequencer signature (ECDSA signature of block hash)
	Signature []byte `json:"signature"`
}

// GetHeight returns the block number as int64 for compatibility.
func (b *BlockV2) GetHeight() int64 {
	return int64(b.Number)
}

// GetHash returns the block hash as bytes.
func (b *BlockV2) GetHash() []byte {
	return b.Hash.Bytes()
}

// SyncableBlock is an interface that both old Block and new BlockV2 can implement
// for compatibility in the block pool.
type SyncableBlock interface {
	GetHeight() int64
	GetHash() []byte
}

// Ensure BlockV2 implements SyncableBlock
var _ SyncableBlock = (*BlockV2)(nil)

// SequencerAddress is the expected sequencer address for signature verification.
// This will be set by the sequencer package at init time.
var SequencerAddress common.Address

// SetSequencerAddress sets the expected sequencer address.
func SetSequencerAddress(addr common.Address) {
	SequencerAddress = addr
}

// IsSequencerAddress checks if the given address is the expected sequencer.
func IsSequencerAddress(addr common.Address) bool {
	return addr == SequencerAddress
}

// RecoverBlockV2Signer recovers the signer address from the block's signature.
func RecoverBlockV2Signer(block *BlockV2) (common.Address, error) {
	if len(block.Signature) == 0 {
		return common.Address{}, fmt.Errorf("block has no signature")
	}

	// Recover the public key from the signature
	pubKey, err := crypto.SigToPub(block.Hash.Bytes(), block.Signature)
	if err != nil {
		return common.Address{}, fmt.Errorf("failed to recover public key: %w", err)
	}

	return crypto.PubkeyToAddress(*pubKey), nil
}

// BlockV2FromProto converts a proto BlockV2 to types.BlockV2.
func BlockV2FromProto(pb *seqproto.BlockV2) (*BlockV2, error) {
	if pb == nil {
		return nil, errors.New("nil BlockV2")
	}

	// Basic validation
	if len(pb.ParentHash) != 32 {
		return nil, errors.New("invalid parent hash length")
	}
	if len(pb.Hash) != 32 {
		return nil, errors.New("invalid block hash length")
	}

	baseFee := new(big.Int)
	if len(pb.BaseFee) > 0 {
		baseFee.SetBytes(pb.BaseFee)
	}

	return &BlockV2{
		ParentHash:         common.BytesToHash(pb.ParentHash),
		Miner:              common.BytesToAddress(pb.Miner),
		Number:             pb.Number,
		GasLimit:           pb.GasLimit,
		BaseFee:            baseFee,
		Timestamp:          pb.Timestamp,
		Transactions:       pb.Transactions,
		StateRoot:          common.BytesToHash(pb.StateRoot),
		GasUsed:            pb.GasUsed,
		ReceiptRoot:        common.BytesToHash(pb.ReceiptRoot),
		LogsBloom:          pb.LogsBloom,
		WithdrawTrieRoot:   common.BytesToHash(pb.WithdrawTrieRoot),
		NextL1MessageIndex: pb.NextL1MessageIndex,
		Hash:               common.BytesToHash(pb.Hash),
		Signature:          pb.Signature,
	}, nil
}

// BlockV2ToProto converts types.BlockV2 to proto BlockV2.
func BlockV2ToProto(block *BlockV2) *seqproto.BlockV2 {
	if block == nil {
		return nil
	}

	var baseFeeBytes []byte
	if block.BaseFee != nil {
		baseFeeBytes = block.BaseFee.Bytes()
	}

	return &seqproto.BlockV2{
		ParentHash:         block.ParentHash.Bytes(),
		Miner:              block.Miner.Bytes(),
		Number:             block.Number,
		GasLimit:           block.GasLimit,
		BaseFee:            baseFeeBytes,
		Timestamp:          block.Timestamp,
		Transactions:       block.Transactions,
		StateRoot:          block.StateRoot.Bytes(),
		GasUsed:            block.GasUsed,
		ReceiptRoot:        block.ReceiptRoot.Bytes(),
		LogsBloom:          block.LogsBloom,
		WithdrawTrieRoot:   block.WithdrawTrieRoot.Bytes(),
		NextL1MessageIndex: block.NextL1MessageIndex,
		Hash:               block.Hash.Bytes(),
		Signature:          block.Signature,
	}
}
