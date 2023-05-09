package l2node

import (
	"math/rand"

	"github.com/tendermint/tendermint/types"
)

type L2Node interface {
	RequestBlockData(
		height int64,
	) (
		txs [][]byte,
		l2Config []byte,
		zkConfig []byte,
		err error,
	)

	CheckBlockData(
		txs [][]byte,
		l2Config []byte,
		zkConfig []byte,
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
		currentHeight int64,
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

var _ L2Node = &MockL2Node{}

type MockL2Node struct {
	txNumber      int
	currentHeight int64
}

func NewMockL2Node(n int, h int64) L2Node {
	return &MockL2Node{
		txNumber:      n,
		currentHeight: h,
	}
}

func (l *MockL2Node) SetCurrentHeight(h int64) {
	l.currentHeight = h
}

func (l *MockL2Node) SetTxNumber(n int) {
	l.txNumber = n
}

func (l *MockL2Node) RequestBlockData(
	height int64,
) (
	txs [][]byte,
	l2Config []byte,
	zkConfig []byte,
	err error,
) {
	l.currentHeight = height
	var rTxs [][]byte
	for i := int(0); i < l.txNumber; i++ {
		rTxs = append(rTxs, randBytes(10))
	}
	lc := randBytes(8)
	zc := randBytes(8)
	return rTxs, lc, zc, nil
}

func (l MockL2Node) CheckBlockData(
	txs [][]byte,
	l2Config []byte,
	zkConfig []byte,
) (
	valid bool,
	err error,
) {
	return true, nil
}

func (l MockL2Node) DeliverBlock(
	txs [][]byte,
	l2Config []byte,
	zkConfig []byte,
	validators []types.Address,
	blsSignatures [][]byte,
) (
	currentHeight int64,
	err error,
) {
	return l.currentHeight, nil
}

func randBytes(n int) []byte {
	bytes := make([]byte, n)
	rand.Read(bytes)
	return bytes
}
