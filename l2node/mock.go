package l2node

import (
	"math/rand"
)

var _ L2Node = &MockL2Node{}

type MockL2Node struct {
	txNumber int
}

func NewMockL2Node(n int) L2Node {
	return &MockL2Node{
		txNumber: n,
	}
}

func (l *MockL2Node) SetTxNumber(n int) {
	l.txNumber = n
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

func (l *MockL2Node) RequestBlockData(
	height int64,
) (
	txs [][]byte,
	configs Configs,
	err error,
) {
	for i := int(0); i < l.txNumber; i++ {
		txs = append(txs, randBytes(10))
	}
	configs.L2Config = randBytes(8)
	configs.ZKConfig = randBytes(8)
	configs.Root = randBytes(8)
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
	nextBatchParams BatchParams,
	nextValidatorSet [][]byte,
	err error,
) {
	// TODO nextBatchParams
	// TODO nextValidatorSet
	return
}

func randBytes(n int) []byte {
	bytes := make([]byte, n)
	rand.Read(bytes)
	return bytes
}
