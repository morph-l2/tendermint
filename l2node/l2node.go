package l2node

import (
	"fmt"
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

func (l *MockL2Node) RequestBlockData(
	height int64,
) (
	txs [][]byte,
	l2Config []byte,
	zkConfig []byte,
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
	var lc []byte
	var zc []byte
	if l.txNumber > 0 {
		lc = randBytes(8)
		zc = randBytes(8)
	}
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
	fmt.Println("============================================================")
	fmt.Println("CheckBlockData")
	fmt.Println(txs)
	fmt.Println(l2Config)
	fmt.Println(zkConfig)
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
