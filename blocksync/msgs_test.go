package blocksync_test

import (
	"bytes"
	"encoding/hex"
	"math"
	"math/big"
	"testing"

	"github.com/cosmos/gogoproto/proto"
	"github.com/morph-l2/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tendermint/tendermint/blocksync"
	"github.com/tendermint/tendermint/crypto"
	bcproto "github.com/tendermint/tendermint/proto/tendermint/blocksync"
	"github.com/tendermint/tendermint/types"
	"github.com/tendermint/tendermint/upgrade"
)

func TestBcBlockRequestMessageValidateBasic(t *testing.T) {
	testCases := []struct {
		testName      string
		requestHeight int64
		expectErr     bool
	}{
		{"Valid Request Message", 0, false},
		{"Valid Request Message", 1, false},
		{"Invalid Request Message", -1, true},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.testName, func(t *testing.T) {
			request := bcproto.BlockRequest{Height: tc.requestHeight}
			assert.Equal(t, tc.expectErr, blocksync.ValidateMsg(&request) != nil, "Validate Basic had an unexpected result")
		})
	}
}

func TestBcNoBlockResponseMessageValidateBasic(t *testing.T) {
	testCases := []struct {
		testName          string
		nonResponseHeight int64
		expectErr         bool
	}{
		{"Valid Non-Response Message", 0, false},
		{"Valid Non-Response Message", 1, false},
		{"Invalid Non-Response Message", -1, true},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.testName, func(t *testing.T) {
			nonResponse := bcproto.NoBlockResponse{Height: tc.nonResponseHeight}
			assert.Equal(t, tc.expectErr, blocksync.ValidateMsg(&nonResponse) != nil, "Validate Basic had an unexpected result")
		})
	}
}

func TestBcStatusRequestMessageValidateBasic(t *testing.T) {
	request := bcproto.StatusRequest{}
	assert.NoError(t, blocksync.ValidateMsg(&request))
}

func TestBcStatusResponseMessageValidateBasic(t *testing.T) {
	testCases := []struct {
		testName       string
		responseHeight int64
		expectErr      bool
	}{
		{"Valid Response Message", 0, false},
		{"Valid Response Message", 1, false},
		{"Invalid Response Message", -1, true},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.testName, func(t *testing.T) {
			response := bcproto.StatusResponse{Height: tc.responseHeight}
			assert.Equal(t, tc.expectErr, blocksync.ValidateMsg(&response) != nil, "Validate Basic had an unexpected result")
		})
	}
}

func TestValidateMsgBlockResponseRejectsV1AtUpgradeHeight(t *testing.T) {
	oldHeight := upgrade.UpgradeBlockHeight
	upgrade.SetUpgradeBlockHeight(10)
	defer upgrade.SetUpgradeBlockHeight(oldHeight)

	lastBlockID := types.BlockID{
		Hash: bytes.Repeat([]byte{1}, 32),
		PartSetHeader: types.PartSetHeader{
			Total: 1,
			Hash:  bytes.Repeat([]byte{2}, 32),
		},
	}
	lastCommit := types.NewCommit(9, 0, lastBlockID, []types.CommitSig{{
		BlockIDFlag:      types.BlockIDFlagCommit,
		ValidatorAddress: bytes.Repeat([]byte{3}, crypto.AddressSize),
		Signature:        []byte{1},
	}})

	block := types.MakeBlock(10, []types.Tx{types.Tx("Hello World")}, nil, nil, nil, lastCommit, nil)
	block.ProposerAddress = bytes.Repeat([]byte{4}, crypto.AddressSize)
	bpb, err := block.ToProto()
	require.NoError(t, err)

	err = blocksync.ValidateMsg(&bcproto.BlockResponse{Block: bpb})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected BlockResponse")
}

func TestValidateMsgBlockResponseV2RejectsPreUpgradeHeight(t *testing.T) {
	oldHeight := upgrade.UpgradeBlockHeight
	upgrade.SetUpgradeBlockHeight(10)
	defer upgrade.SetUpgradeBlockHeight(oldHeight)

	blockV2 := &types.BlockV2{
		ParentHash: common.HexToHash("0x1"),
		Hash:       common.HexToHash("0x2"),
		BaseFee:    big.NewInt(1),
		Number:     9,
	}

	err := blocksync.ValidateMsg(&bcproto.BlockResponseV2{Block: types.BlockV2ToProto(blockV2)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected BlockResponseV2")
}

func TestValidateMsgBlockResponseV2AllowsUpgradeHeight(t *testing.T) {
	oldHeight := upgrade.UpgradeBlockHeight
	upgrade.SetUpgradeBlockHeight(10)
	defer upgrade.SetUpgradeBlockHeight(oldHeight)

	blockV2 := &types.BlockV2{
		ParentHash: common.HexToHash("0x1"),
		Hash:       common.HexToHash("0x2"),
		BaseFee:    big.NewInt(1),
		Number:     10,
	}

	err := blocksync.ValidateMsg(&bcproto.BlockResponseV2{Block: types.BlockV2ToProto(blockV2)})
	require.NoError(t, err)
}

//nolint:lll // ignore line length in tests
func TestBlockchainMessageVectors(t *testing.T) {
	block := types.MakeBlock(int64(3), []types.Tx{types.Tx("Hello World")}, nil, nil, nil, nil, nil) // TODO
	block.Version.Block = 11                                                                         // overwrite updated protocol version

	bpb, err := block.ToProto()
	require.NoError(t, err)

	testCases := []struct {
		testName string
		bmsg     proto.Message
		expBytes string
	}{
		{"BlockRequestMessage", &bcproto.Message{Sum: &bcproto.Message_BlockRequest{
			BlockRequest: &bcproto.BlockRequest{Height: 1}}}, "0a020801"},
		{"BlockRequestMessage", &bcproto.Message{Sum: &bcproto.Message_BlockRequest{
			BlockRequest: &bcproto.BlockRequest{Height: math.MaxInt64}}},
			"0a0a08ffffffffffffffff7f"},
		{"BlockResponseMessage", &bcproto.Message{Sum: &bcproto.Message_BlockResponse{
			BlockResponse: &bcproto.BlockResponse{Block: bpb}}}, "1a700a6e0a5b0a02080b1803220b088092b8c398feffffff012a0212003a20c4da88e876062aa1543400d50d0eaa0dac88096057949cfb7bca7f3a48c04bf96a20e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855120d0a0b48656c6c6f20576f726c641a00"},
		{"NoBlockResponseMessage", &bcproto.Message{Sum: &bcproto.Message_NoBlockResponse{
			NoBlockResponse: &bcproto.NoBlockResponse{Height: 1}}}, "12020801"},
		{"NoBlockResponseMessage", &bcproto.Message{Sum: &bcproto.Message_NoBlockResponse{
			NoBlockResponse: &bcproto.NoBlockResponse{Height: math.MaxInt64}}},
			"120a08ffffffffffffffff7f"},
		{"StatusRequestMessage", &bcproto.Message{Sum: &bcproto.Message_StatusRequest{
			StatusRequest: &bcproto.StatusRequest{}}},
			"2200"},
		{"StatusResponseMessage", &bcproto.Message{Sum: &bcproto.Message_StatusResponse{
			StatusResponse: &bcproto.StatusResponse{Height: 1, Base: 2}}},
			"2a0408011002"},
		{"StatusResponseMessage", &bcproto.Message{Sum: &bcproto.Message_StatusResponse{
			StatusResponse: &bcproto.StatusResponse{Height: math.MaxInt64, Base: math.MaxInt64}}},
			"2a1408ffffffffffffffff7f10ffffffffffffffff7f"},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.testName, func(t *testing.T) {
			bz, _ := proto.Marshal(tc.bmsg)

			require.Equal(t, tc.expBytes, hex.EncodeToString(bz))
		})
	}
}
