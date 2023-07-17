package l2node

import (
	"fmt"
	"math/rand"

	"github.com/tendermint/tendermint/types"
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
	fmt.Println("============================================================")
	fmt.Println("RequestHeight")
	fmt.Println(tmHeight)
	fmt.Println(height)
	fmt.Println("============================================================")
	return tmHeight, nil
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
	l2Config []byte,
	zkConfig []byte,
	root []byte,
	err error,
) {
	fmt.Println("============================================================")
	fmt.Println("RequestBlockData")
	fmt.Println(height)
	fmt.Println("============================================================")
	var rTxs [][]byte
	for i := int(0); i < l.txNumber; i++ {
		rTxs = append(rTxs, randBytes(10))
	}
	lc := randBytes(8)
	zc := randBytes(8)
	rt := randBytes(8)
	return rTxs, lc, zc, rt, nil
}

func (l MockL2Node) CheckBlockData(
	txs [][]byte,
	l2Config []byte,
	zkConfig []byte,
	root []byte,
) (
	valid bool,
	err error,
) {
	fmt.Println("============================================================")
	fmt.Println("CheckBlockData")
	fmt.Println(txs)
	fmt.Println(l2Config)
	fmt.Println(zkConfig)
	fmt.Println(root)
	fmt.Println("============================================================")
	return true, nil
}

func (l MockL2Node) DeliverBlock(
	txs [][]byte,
	l2Config []byte,
	zkConfig []byte,
	validators []types.Address,
	blsSignatures [][]byte,
) (
	err error,
) {
	fmt.Println("============================================================")
	fmt.Println("DeliverBlock")
	fmt.Println(txs)
	fmt.Println(l2Config)
	fmt.Println(zkConfig)
	fmt.Println(validators)
	fmt.Println(blsSignatures)
	fmt.Println("============================================================")
	return nil
}

func randBytes(n int) []byte {
	bytes := make([]byte, n)
	rand.Read(bytes)
	return bytes
}
