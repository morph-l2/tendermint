package l2node

import (
	"fmt"

	tmproto "github.com/tendermint/tendermint/proto/tendermint/types"
	"github.com/tendermint/tendermint/types"
)

type L2Node interface {
	Batcher
	RequestHeight(
		tmHeight int64,
	) (
		height int64,
		err error,
	)

	EncodeTxs(
		batchTxs [][]byte,
	) (
		encodedTxs []byte,
		err error,
	)

	RequestBlockData(
		height int64,
		publicKey []byte,
	) (
		txs [][]byte,
		blockMeta []byte,
		collectedL1Msgs bool,
		err error,
	)

	CheckBlockData(
		txs [][]byte,
		blockMeta []byte,
	) (
		valid bool,
		err error,
	)

	DeliverBlock(
		txs [][]byte,
		blockMeta []byte,
		consensusData ConsensusData,
	) (
		nextBatchParams *tmproto.BatchParams, // set nil if no update
		nextValidatorSet [][]byte,
		err error,
	)

	VerifySignature(
		tmKey []byte,
		message []byte, // batch context hash
		signature []byte,
	) (
		valid bool,
		err error,
	)
}

// Batcher is used to pack the blocks into a batch, and commit the batch if it is determined to be a batchPoint.
type Batcher interface {
	CalculateCapWithProposalBlock(
		proposalBlockBytes []byte,
		proposalTxs types.Txs,
		get GetFromBatchStartFunc,
	) (
		sizeExceeded bool,
		err error,
	)

	SealBatch() (
		batchHash []byte,
		batchHeader []byte,
		err error,
	)

	CommitBatch(
		currentBlockBytes []byte,
		currentTxs types.Txs,
		blsDatas []BlsData,
	) error

	PackCurrentBlock(
		currentBlockBytes []byte,
		currentTxs types.Txs,
	) error

	AppendBlsData(height int64, batchHash []byte, data BlsData) error

	BatchHash(batchHeader []byte) ([]byte, error)
}
type GetFromBatchStartFunc func() (
	parentBatchHeader []byte,
	blockMetas [][]byte,
	transactions []types.Txs,
	err error,
)

type ConsensusData struct {
	ValidatorSet [][]byte
	BatchHash    []byte
}

type BlsData struct {
	Signer      []byte
	Signature   []byte
	VotingPower int64
}

func ConvertBytesToTxs(txs [][]byte) []types.Tx {
	s := make([]types.Tx, len(txs))
	for i, v := range txs {
		s[i] = v
	}
	return s
}

func ConvertTxsToBytes(txs []types.Tx) [][]byte {
	s := make([][]byte, len(txs))
	for i, v := range txs {
		s[i] = v
	}
	return s
}

func GetBLSDatas(commit *types.Commit, validators *types.ValidatorSet) (blsDatas []BlsData, err error) {
	for _, signature := range commit.Signatures {
		if len(signature.BLSSignature) > 0 {
			_, validator := validators.GetByAddress(signature.ValidatorAddress)
			if validator == nil {
				err = fmt.Errorf("no validator found by addresss: %x", signature.ValidatorAddress)
				return
			}
			blsDatas = append(blsDatas, BlsData{
				Signer:      validator.PubKey.Bytes(),
				Signature:   signature.BLSSignature,
				VotingPower: validator.VotingPower,
			})
		}
	}
	return
}
