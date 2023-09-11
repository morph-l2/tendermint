package l2node

import (
	tmrand "github.com/tendermint/tendermint/libs/rand"
	tmproto "github.com/tendermint/tendermint/proto/tendermint/types"
)

var _ L2Node = &MockL2Node{}

type MockL2Node struct {
	TxNumber int
}

func NewMockL2Node(n int) L2Node {
	return &MockL2Node{
		TxNumber: n,
	}
}

func (l MockL2Node) RequestHeight(
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

func (l MockL2Node) EncodeTxs(
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

func (l MockL2Node) RequestBlockData(
	height int64,
) (
	txs [][]byte,
	configs Configs,
	err error,
) {
	for i := int(0); i < l.TxNumber; i++ {
		txs = append(txs, tmrand.Bytes(10))
	}
	configs.L2Config = tmrand.Bytes(8)
	configs.ZKConfig = tmrand.Bytes(8)
	configs.Root = tmrand.Bytes(8)
	return
}

func (l MockL2Node) CheckBlockData(
	txs [][]byte,
	configs Configs,
) (
	valid bool,
	err error,
) {
	valid = true
	return
}

func (l MockL2Node) DeliverBlock(
	txs [][]byte,
	configs Configs,
	consensusData ConsensusData,
) (
	nextBatchParams *tmproto.BatchParams,
	nextValidatorSet [][]byte,
	err error,
) {
	nextValidatorSet = consensusData.ValidatorSet
	return
}
