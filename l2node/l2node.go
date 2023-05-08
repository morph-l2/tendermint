package l2node

import (
	"github.com/tendermint/tendermint/types"
)

type L2Node interface {
	RequestBlockData(height int64) (txs [][]byte, l2Config []byte, zkConfig []byte, err error)

	CheckBlockData(txs [][]byte, l2Config []byte, zkConfig []byte) (valid bool, err error)

	DeliverBlock(txs [][]byte, l2Config []byte, zkConfig []byte, validators []types.Address, blsSignatures [][]byte) (currentHeight int64, err error)
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

func GetValidators(block *types.Block) []types.Address {
	var validators []types.Address
	for _, signature := range block.LastCommit.Signatures {
		validators = append(validators, signature.BLSSignature)
	}
	return validators
}

func GetBLSSignatures(block *types.Block) [][]byte {
	var blsSignatures [][]byte
	for _, signature := range block.LastCommit.Signatures {
		blsSignatures = append(blsSignatures, signature.BLSSignature)
	}
	return blsSignatures
}
