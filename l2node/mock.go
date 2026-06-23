package l2node

import (
	"encoding/binary"
	"fmt"
	"os"

	"github.com/morph-l2/go-ethereum/common"
	tmjson "github.com/tendermint/tendermint/libs/json"
	tmos "github.com/tendermint/tendermint/libs/os"
	tmrand "github.com/tendermint/tendermint/libs/rand"
)

var _ L2Node = &MockL2Node{}

type MockL2Node struct {
	TxNumber         int
	ValidatorSetFile string      // used to control the validator set updates
	maxAppliedHeight uint64      // tracks highest block applied (for V2 idempotent check)
	maxAppliedHash   common.Hash // hash at maxAppliedHeight, so same-height reorg blocks aren't skipped
}

func NewMockL2Node(n int, validatorSetFile string) L2Node {
	return &MockL2Node{
		TxNumber:         n,
		ValidatorSetFile: validatorSetFile,
	}
}

func (l *MockL2Node) RequestHeight(
	tmHeight int64,
) (
	height int64,
	err error,
) {
	height = tmHeight
	if tmHeight > 10 {
		height = tmHeight - 2
	}
	return
}

func (l *MockL2Node) EncodeTxs(
	batchTxs [][]byte,
) (
	encodedTxs []byte,
	err error,
) {
	for _, tx := range batchTxs {
		encodedTxs = append(encodedTxs, tx...)
	}
	return
}

func heightToBytes(height int64) []byte {
	enc := make([]byte, 8)
	binary.BigEndian.PutUint64(enc, uint64(height))
	return enc
}

func heightFromBytes(bz []byte) int64 {
	height := binary.BigEndian.Uint64(bz)
	return int64(height)
}

func (l *MockL2Node) RequestBlockData(
	height int64,
) (
	txs [][]byte,
	meta []byte,
	collectedL1Msgs bool,
	err error,
) {
	fmt.Printf(">>>>>>>>>>>>>>>>>[RequestBlockData]I am a validator, height: %d \n", height)
	for i := int(0); i < l.TxNumber; i++ {
		txs = append(txs, tmrand.Bytes(10))
	}
	meta = heightToBytes(height)
	return
}

func (l *MockL2Node) CheckBlockData(
	txs [][]byte,
	meta []byte,
) (
	valid bool,
	err error,
) {
	fmt.Printf(">>>>>>>>>>>>>>>>>[CheckBlockData]I am a validator, current height: %d \n", heightFromBytes(meta))
	valid = true
	return
}

type FileValidatorSet struct {
	Height     int64
	Validators [][]byte
}

func (l *MockL2Node) DeliverBlock(
	txs [][]byte,
	meta []byte,
	consensusData ConsensusData,
) (
	nextValidatorSet [][]byte,
	err error,
) {
	curHeight := heightFromBytes(meta)
	nextValidatorSet = consensusData.ValidatorSet
	if tmos.FileExists(l.ValidatorSetFile) {
		func() {
			vsJSONBytes, err := os.ReadFile(l.ValidatorSetFile)
			if err != nil {
				tmos.Exit(err.Error())
			}
			if len(vsJSONBytes) == 0 {
				return
			}
			vs := FileValidatorSet{}
			err = tmjson.Unmarshal(vsJSONBytes, &vs)
			if err != nil {
				tmos.Exit(fmt.Sprintf("Error reading validator set from %v: %v\n", l.ValidatorSetFile, err))
			}
			if curHeight < vs.Height {
				fmt.Printf(">>>>>>>>>>>>>>>>>validatorSet will be updated at %d, current height: %d \n", vs.Height, curHeight)
			} else if curHeight == vs.Height {
				nextValidatorSet = vs.Validators
				fmt.Printf(">>>>>>>>>>>>>>>>>validatorSet updated, current height: %d \n", curHeight)
			}
		}()
	}

	return nextValidatorSet, err
}

// ==================== V2 Methods for Sequencer Mode ====================

func (l *MockL2Node) RequestBlockDataV2(parentHash []byte) (*BlockV2, bool, error) {
	// Mock implementation: return a simple block
	return &BlockV2{
		Number:    1,
		Timestamp: 0,
	}, false, nil
}

func (l *MockL2Node) ApplyBlockV2(block *BlockV2) (bool, error) {
	// Skip only a true duplicate (same height AND same hash). A same-height
	// block with a different hash is a reorg and must be applied; otherwise
	// reorg unit tests get false-positive passes.
	if block.Number < l.maxAppliedHeight ||
		(block.Number == l.maxAppliedHeight && block.Hash == l.maxAppliedHash) {
		return false, nil
	}
	l.maxAppliedHeight = block.Number
	l.maxAppliedHash = block.Hash
	return true, nil
}

func (l *MockL2Node) GetBlockByNumber(height uint64) (*BlockV2, error) {
	// Mock implementation: return a simple block
	return &BlockV2{
		Number:    height,
		Timestamp: 0,
	}, nil
}

func (l *MockL2Node) GetLatestBlockV2() (*BlockV2, error) {
	// Mock implementation: return block 0
	return &BlockV2{
		Number:    0,
		Timestamp: 0,
	}, nil
}
