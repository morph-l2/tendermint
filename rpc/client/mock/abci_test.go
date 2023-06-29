package mock_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tendermint/tendermint/abci/example/kvstore"
	abci "github.com/tendermint/tendermint/abci/types"
	"github.com/tendermint/tendermint/libs/bytes"
	"github.com/tendermint/tendermint/rpc/client"
	"github.com/tendermint/tendermint/rpc/client/mock"
	ctypes "github.com/tendermint/tendermint/rpc/core/types"
	"github.com/tendermint/tendermint/types"
)

func TestABCIMock(t *testing.T) {
	assert, require := assert.New(t), require.New(t)

	key, value := []byte("foo"), []byte("bar")
	height := int64(10)

	m := mock.ABCIMock{
		Info: mock.Call{Error: errors.New("foobar")},
		Query: mock.Call{Response: abci.ResponseQuery{
			Key:    key,
			Value:  value,
			Height: height,
		}},
		Broadcast: mock.Call{Error: errors.New("must commit")},
	}

	// now, let's try to make some calls
	_, err := m.ABCIInfo(context.Background())
	require.NotNil(err)
	assert.Equal("foobar", err.Error())

	// query always returns the response
	_query, err := m.ABCIQueryWithOptions(context.Background(), "/", nil, client.ABCIQueryOptions{Prove: false})
	query := _query.Response
	require.Nil(err)
	require.NotNil(query)
	assert.EqualValues(key, query.Key)
	assert.EqualValues(value, query.Value)
	assert.Equal(height, query.Height)
}

func TestABCIRecorder(t *testing.T) {
	assert, require := assert.New(t), require.New(t)

	// This mock returns errors on everything but Query
	m := mock.ABCIMock{
		Info: mock.Call{Response: abci.ResponseInfo{
			Data:    "data",
			Version: "v0.9.9",
		}},
		Query:     mock.Call{Error: errors.New("query")},
		Broadcast: mock.Call{Error: errors.New("broadcast")},
	}
	r := mock.NewABCIRecorder(m)

	require.Equal(0, len(r.Calls))

	_, err := r.ABCIInfo(context.Background())
	assert.Nil(err, "expected no err on info")

	_, err = r.ABCIQueryWithOptions(
		context.Background(),
		"path",
		bytes.HexBytes("data"),
		client.ABCIQueryOptions{Prove: false},
	)
	assert.NotNil(err, "expected error on query")
	require.Equal(2, len(r.Calls))

	info := r.Calls[0]
	assert.Equal("abci_info", info.Name)
	assert.Nil(info.Error)
	assert.Nil(info.Args)
	require.NotNil(info.Response)
	ir, ok := info.Response.(*ctypes.ResultABCIInfo)
	require.True(ok)
	assert.Equal("data", ir.Response.Data)
	assert.Equal("v0.9.9", ir.Response.Version)

	query := r.Calls[1]
	assert.Equal("abci_query", query.Name)
	assert.Nil(query.Response)
	require.NotNil(query.Error)
	assert.Equal("query", query.Error.Error())
	require.NotNil(query.Args)
	qa, ok := query.Args.(mock.QueryArgs)
	require.True(ok)
	assert.Equal("path", qa.Path)
	assert.EqualValues("data", qa.Data)
	assert.False(qa.Prove)

	// now add some broadcasts (should all err)
	txs := []types.Tx{{1}, {2}, {3}}

	require.Equal(5, len(r.Calls))

	bc := r.Calls[2]
	assert.Equal("broadcast_tx_commit", bc.Name)
	assert.Nil(bc.Response)
	require.NotNil(bc.Error)
	assert.EqualValues(bc.Args, txs[0])

	bs := r.Calls[3]
	assert.Equal("broadcast_tx_sync", bs.Name)
	assert.Nil(bs.Response)
	require.NotNil(bs.Error)
	assert.EqualValues(bs.Args, txs[1])

	ba := r.Calls[4]
	assert.Equal("broadcast_tx_async", ba.Name)
	assert.Nil(ba.Response)
	require.NotNil(ba.Error)
	assert.EqualValues(ba.Args, txs[2])
}

func TestABCIApp(t *testing.T) {
	assert, require := assert.New(t), require.New(t)
	app := kvstore.NewApplication()
	m := mock.ABCIApp{app}

	// get some info
	info, err := m.ABCIInfo(context.Background())
	require.Nil(err)
	assert.Equal(`{"size":0}`, info.Response.GetData())
}
