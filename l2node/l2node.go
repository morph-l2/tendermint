package l2node

import (
	"github.com/tendermint/tendermint/types"
)

type L2Node interface {
	RequestBlockData(height int64) (txs [][]byte, l2Config []byte, zkConfig []byte, err error)

	CheckBlockData(txs [][]byte, l2Config []byte, zkConfig []byte) (valid bool, err error)

	DeliverBlock(txs [][]byte, l2Config []byte, zkConfig []byte, validators []types.Address, blsSignatures [][]byte) (currentHeight int64, err error)
}
