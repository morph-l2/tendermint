package l2node

import (
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/tendermint/tendermint/crypto/tmhash"
	"github.com/tendermint/tendermint/types"
	"os"

	tmjson "github.com/tendermint/tendermint/libs/json"
	tmos "github.com/tendermint/tendermint/libs/os"
	tmrand "github.com/tendermint/tendermint/libs/rand"
	tmproto "github.com/tendermint/tendermint/proto/tendermint/types"
)

var _ L2Node = &MockL2Node{}

var genesisParentBatchHeader = []byte{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}

type MockL2Node struct {
	TxNumber                 int
	ValidatorSetFile         string // used to control the validator set updates
	genesisParentBatchHeader []byte

	parentBatchHeader        []byte
	encodingBatch            []byte // parentBatchHeader|block|txs|...|block|txs|...
	currentBlockWithTxsBytes []byte
	sealedBatchHeader        []byte
	committedBatches         map[[tmhash.Size]byte][]byte // batchHash -> batchHeader
}

func NewMockL2Node(n int, validatorSetFile string) L2Node {
	return &MockL2Node{
		TxNumber:                 n,
		ValidatorSetFile:         validatorSetFile,
		genesisParentBatchHeader: genesisParentBatchHeader,
		committedBatches:         make(map[[tmhash.Size]byte][]byte),
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
	nextBatchParams *tmproto.BatchParams,
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

	return
}

func (l *MockL2Node) VerifySignature(
	tmKey []byte,
	message []byte,
	signature []byte,
) (
	valid bool,
	err error,
) {
	return true, nil
}

func (l *MockL2Node) CalculateBatchSizeWithProposalBlock(
	proposalBlockBytes []byte,
	proposalTxs types.Txs,
	get GetFromBatchStartFunc,
) (
	batchSize int64,
	err error,
) {
	if len(proposalBlockBytes) < 8 {
		return 0, errors.New("empty block bytes")
	}
	if len(l.encodingBatch) == 0 {
		parentBatchHeader, blockMetas, transactions, err := get()
		if err != nil {
			return 0, err
		}
		if len(parentBatchHeader) == 0 {
			l.parentBatchHeader = l.genesisParentBatchHeader
		} else {
			l.parentBatchHeader = parentBatchHeader
		}
		l.encodingBatch = append(l.encodingBatch, l.parentBatchHeader...)
		for i, blockMeta := range blockMetas {
			l.encodingBatch = append(l.encodingBatch, blockMeta...)
			for _, tx := range transactions[i] {
				l.encodingBatch = append(l.encodingBatch, tx...)
			}
		}
	}
	l.currentBlockWithTxsBytes = proposalBlockBytes
	for _, tx := range proposalTxs {
		l.currentBlockWithTxsBytes = append(l.currentBlockWithTxsBytes, tx...)
	}
	return int64(len(l.encodingBatch) + len(l.currentBlockWithTxsBytes)), err
}

func (l *MockL2Node) SealBatch() ([]byte, []byte, error) {
	if len(l.encodingBatch) < 32+8 { // header length + 1 blockMeta length(8bytes)
		return nil, nil, errors.New("wrong length batch")
	}
	batchHeader := l.encodingBatch[8:40] // make sure header has 32 bytes
	batchHash := tmhash.Sum(batchHeader)

	l.sealedBatchHeader = batchHeader
	return batchHash, batchHeader, nil
}

func (l *MockL2Node) CommitBatch(
	currentBlockBytes []byte,
	currentTxs types.Txs,
	datas []BlsData,
) error {
	if len(l.sealedBatchHeader) == 0 {
		return nil
	}
	batchHeader := l.sealedBatchHeader
	batchHashBytes := tmhash.Sum(batchHeader)
	var batchHash [tmhash.Size]byte
	copy(batchHash[:], batchHashBytes)

	// commit current batch header
	// update parentBatchHeader to committed batch header
	// move current block bytes to a new batch
	// remove previous sealedBatchHeader
	l.committedBatches[batchHash] = batchHeader
	l.parentBatchHeader = batchHeader
	l.encodingBatch = append(l.parentBatchHeader, l.currentBlockWithTxsBytes...)
	l.currentBlockWithTxsBytes = nil
	l.sealedBatchHeader = nil
	return nil
}

func (l *MockL2Node) PackCurrentBlock(
	currentBlockBytes []byte,
	currentTxs types.Txs,
) error {
	l.encodingBatch = append(l.encodingBatch, l.currentBlockWithTxsBytes...)
	l.currentBlockWithTxsBytes = nil
	return nil
}

func (l *MockL2Node) AppendBlsData(height int64, batchHash []byte, data BlsData) error {
	return nil
}

func (l *MockL2Node) BatchHash(batchHeader []byte) ([]byte, error) {
	return tmhash.Sum(batchHeader), nil
}
