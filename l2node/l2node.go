package l2node

import (
	"github.com/tendermint/tendermint/types"
)

type L2Node interface {
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
		configs Configs,
		err error,
	)

	CheckBlockData(
		txs [][]byte,
		configs Configs,
	) (
		valid bool,
		err error,
	)

	DeliverBlock(
		txs [][]byte,
		configs Configs,
		consensusData ConsensusData,
	) (
		nextBatchParams BatchParams,
		nextValidatorSet [][]byte,
		err error,
	)
}

type Configs struct {
	L2Config []byte
	ZKConfig []byte
	Root     []byte
}

type BatchParams struct {
	BatchBlocksInterval int64
	BatchMaxBytes       int64
	BatchTimeout        int64
}

type ConsensusData struct {
	ValidatorSet  [][]byte
	Validators    [][]byte
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

func GetValidators(commit *types.Commit) (validators []types.Address) {
	for _, signature := range commit.Signatures {
		validators = append(validators, signature.ValidatorAddress)
	}
	return validators
}

func GetBLSSignatures(commit *types.Commit) (blsSignatures [][]byte) {
	for _, signature := range commit.Signatures {
		blsSignatures = append(blsSignatures, signature.BLSSignature)
	}
	return blsSignatures
}
