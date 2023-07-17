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
		l2Config []byte,
		zkConfig []byte,
		root []byte,
		err error,
	)

	CheckBlockData(
		txs [][]byte,
		l2Config []byte,
		zkConfig []byte,
		root []byte,
	) (
		valid bool,
		err error,
	)

	DeliverBlock(
		txs [][]byte,
		l2Config []byte,
		zkConfig []byte,
		validators []types.Address,
		blsSignatures [][]byte,
	) (
		err error,
	)
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

func GetValidators(commit *types.Commit) []types.Address {
	var validators []types.Address
	// fmt.Println("===========================")
	for _, signature := range commit.Signatures {
		// fmt.Println(len(signature.ValidatorAddress))
		// TODO return err if len(signature.ValidatorAddress) == 0 {}
		validators = append(validators, signature.ValidatorAddress)
	}
	return validators
}

func GetBLSSignatures(commit *types.Commit) [][]byte {
	var blsSignatures [][]byte
	// fmt.Println("===========================")
	for _, signature := range commit.Signatures {
		// fmt.Println(len(signature.BLSSignature))
		// TODO return err if len(signature.BLSSignature) == 0
		blsSignatures = append(blsSignatures, signature.BLSSignature)
	}
	return blsSignatures
}
