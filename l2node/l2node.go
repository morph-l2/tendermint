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
	CalculateBatchSizeWithProposalBlock(
		proposalBlockBytes []byte,
		proposalTxs types.Txs,
		get GetFromBatchStartFunc,
	) (
		batchSize int64,
		err error,
	)

	SealBatch() (
		batchHash []byte,
		batchHeader []byte,
		err error,
	)

	CommitBatch() error

	PackCurrentBlock() error

	BatchHash(batchHeader []byte) ([]byte, error)
}
type GetFromBatchStartFunc func() (
	parentBatchHeader []byte,
	blockMetas [][]byte,
	transactions []types.Txs,
	err error,
)

type Configs struct {
	L2Config []byte
	ZKConfig []byte
	Root     []byte
}

type ConsensusData struct {
	ValidatorSet  [][]byte
	BlsSigners    [][]byte
	BlsSignatures [][]byte
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

func GetBlsSigners(commit *types.Commit) (blsSigners []types.Address) {
	for _, signature := range commit.Signatures {
		if len(signature.BLSSignature) > 0 {
			blsSigners = append(blsSigners, signature.ValidatorAddress)
		}
	}
	return blsSigners
}

func GetBLSSignatures(commit *types.Commit) (blsSignatures [][]byte) {
	for _, signature := range commit.Signatures {
		if len(signature.BLSSignature) > 0 {
			blsSignatures = append(blsSignatures, signature.BLSSignature)
		}
	}
	return blsSignatures
}

func GetBLSData(commit *types.Commit, validators *types.ValidatorSet) (blsSigners, blsSignatures [][]byte, votingPowers []int64, err error) {
	for _, signature := range commit.Signatures {
		if len(signature.BLSSignature) > 0 {
			blsSignatures = append(blsSignatures, signature.BLSSignature)
			_, validator := validators.GetByAddress(signature.ValidatorAddress)
			if validator == nil {
				err = fmt.Errorf("no validator found by addresss: %x", signature.ValidatorAddress)
				return
			}
			blsSigners = append(blsSigners, validator.PubKey.Bytes())
			votingPowers = append(votingPowers, validator.VotingPower)
		}
	}
	return
}
