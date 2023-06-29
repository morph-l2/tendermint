package client_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tmjson "github.com/tendermint/tendermint/libs/json"
	"github.com/tendermint/tendermint/libs/log"
	"github.com/tendermint/tendermint/rpc/client"
	rpchttp "github.com/tendermint/tendermint/rpc/client/http"
	rpclocal "github.com/tendermint/tendermint/rpc/client/local"
	ctypes "github.com/tendermint/tendermint/rpc/core/types"
	rpcclient "github.com/tendermint/tendermint/rpc/jsonrpc/client"
	rpctest "github.com/tendermint/tendermint/rpc/test"
	"github.com/tendermint/tendermint/types"
)

var (
	ctx = context.Background()
)

func getHTTPClient() *rpchttp.HTTP {
	rpcAddr := rpctest.GetConfig().RPC.ListenAddress
	c, err := rpchttp.New(rpcAddr, "/websocket")
	if err != nil {
		panic(err)
	}
	c.SetLogger(log.TestingLogger())
	return c
}

func getHTTPClientWithTimeout(timeout uint) *rpchttp.HTTP {
	rpcAddr := rpctest.GetConfig().RPC.ListenAddress
	c, err := rpchttp.NewWithTimeout(rpcAddr, "/websocket", timeout)
	if err != nil {
		panic(err)
	}
	c.SetLogger(log.TestingLogger())
	return c
}

func getLocalClient() *rpclocal.Local {
	return rpclocal.New(node)
}

// GetClients returns a slice of clients for table-driven tests
func GetClients() []client.Client {
	return []client.Client{
		getHTTPClient(),
		getLocalClient(),
	}
}

func TestNilCustomHTTPClient(t *testing.T) {
	require.Panics(t, func() {
		_, _ = rpchttp.NewWithClient("http://example.com", "/websocket", nil)
	})
	require.Panics(t, func() {
		_, _ = rpcclient.NewWithHTTPClient("http://example.com", nil)
	})
}

func TestCustomHTTPClient(t *testing.T) {
	remote := rpctest.GetConfig().RPC.ListenAddress
	c, err := rpchttp.NewWithClient(remote, "/websocket", http.DefaultClient)
	require.Nil(t, err)
	status, err := c.Status(context.Background())
	require.NoError(t, err)
	require.NotNil(t, status)
}

func TestCorsEnabled(t *testing.T) {
	origin := rpctest.GetConfig().RPC.CORSAllowedOrigins[0]
	remote := strings.ReplaceAll(rpctest.GetConfig().RPC.ListenAddress, "tcp", "http")

	req, err := http.NewRequest("GET", remote, nil)
	require.Nil(t, err, "%+v", err)
	req.Header.Set("Origin", origin)
	c := &http.Client{}
	resp, err := c.Do(req)
	require.Nil(t, err, "%+v", err)
	defer resp.Body.Close()

	assert.Equal(t, resp.Header.Get("Access-Control-Allow-Origin"), origin)
}

// Make sure status is correct (we connect properly)
func TestStatus(t *testing.T) {
	for i, c := range GetClients() {
		moniker := rpctest.GetConfig().Moniker
		status, err := c.Status(context.Background())
		require.Nil(t, err, "%d: %+v", i, err)
		assert.Equal(t, moniker, status.NodeInfo.Moniker)
	}
}

// Make sure info is correct (we connect properly)
func TestInfo(t *testing.T) {
	for i, c := range GetClients() {
		// status, err := c.Status()
		// require.Nil(t, err, "%+v", err)
		info, err := c.ABCIInfo(context.Background())
		require.Nil(t, err, "%d: %+v", i, err)
		// TODO: this is not correct - fix merkleeyes!
		// assert.EqualValues(t, status.SyncInfo.LatestBlockHeight, info.Response.LastBlockHeight)
		assert.True(t, strings.Contains(info.Response.Data, "size"))
	}
}

func TestNetInfo(t *testing.T) {
	for i, c := range GetClients() {
		nc, ok := c.(client.NetworkClient)
		require.True(t, ok, "%d", i)
		netinfo, err := nc.NetInfo(context.Background())
		require.Nil(t, err, "%d: %+v", i, err)
		assert.True(t, netinfo.Listening)
		assert.Equal(t, 0, len(netinfo.Peers))
	}
}

func TestDumpConsensusState(t *testing.T) {
	for i, c := range GetClients() {
		// FIXME: fix server so it doesn't panic on invalid input
		nc, ok := c.(client.NetworkClient)
		require.True(t, ok, "%d", i)
		cons, err := nc.DumpConsensusState(context.Background())
		require.Nil(t, err, "%d: %+v", i, err)
		assert.NotEmpty(t, cons.RoundState)
		assert.Empty(t, cons.Peers)
	}
}

func TestConsensusState(t *testing.T) {
	for i, c := range GetClients() {
		// FIXME: fix server so it doesn't panic on invalid input
		nc, ok := c.(client.NetworkClient)
		require.True(t, ok, "%d", i)
		cons, err := nc.ConsensusState(context.Background())
		require.Nil(t, err, "%d: %+v", i, err)
		assert.NotEmpty(t, cons.RoundState)
	}
}

func TestHealth(t *testing.T) {
	for i, c := range GetClients() {
		nc, ok := c.(client.NetworkClient)
		require.True(t, ok, "%d", i)
		_, err := nc.Health(context.Background())
		require.Nil(t, err, "%d: %+v", i, err)
	}
}

func TestGenesisAndValidators(t *testing.T) {
	for i, c := range GetClients() {

		// make sure this is the right genesis file
		gen, err := c.Genesis(context.Background())
		require.Nil(t, err, "%d: %+v", i, err)
		// get the genesis validator
		require.Equal(t, 1, len(gen.Genesis.Validators))
		gval := gen.Genesis.Validators[0]

		// get the current validators
		h := int64(1)
		vals, err := c.Validators(context.Background(), &h, nil, nil)
		require.Nil(t, err, "%d: %+v", i, err)
		require.Equal(t, 1, len(vals.Validators))
		require.Equal(t, 1, vals.Count)
		require.Equal(t, 1, vals.Total)
		val := vals.Validators[0]

		// make sure the current set is also the genesis set
		assert.Equal(t, gval.Power, val.VotingPower)
		assert.Equal(t, gval.PubKey, val.PubKey)
	}
}

func TestGenesisChunked(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for _, c := range GetClients() {
		first, err := c.GenesisChunked(ctx, 0)
		require.NoError(t, err)

		decoded := make([]string, 0, first.TotalChunks)
		for i := 0; i < first.TotalChunks; i++ {
			chunk, err := c.GenesisChunked(ctx, uint(i))
			require.NoError(t, err)
			data, err := base64.StdEncoding.DecodeString(chunk.Data)
			require.NoError(t, err)
			decoded = append(decoded, string(data))

		}
		doc := []byte(strings.Join(decoded, ""))

		var out types.GenesisDoc
		require.NoError(t, tmjson.Unmarshal(doc, &out),
			"first: %+v, doc: %s", first, string(doc))
	}
}

func TestBatchedJSONRPCCalls(t *testing.T) {
	c := getHTTPClient()
	testBatchedJSONRPCCalls(t, c)
}

func testBatchedJSONRPCCalls(t *testing.T, c *rpchttp.HTTP) {
	k1, v1, _ := MakeTxKV()
	k2, v2, _ := MakeTxKV()

	batch := c.NewBatch()
	require.Equal(t, 2, batch.Count())
	bresults, err := batch.Send(ctx)
	require.NoError(t, err)
	require.Len(t, bresults, 2)
	require.Equal(t, 0, batch.Count())

	q1, err := batch.ABCIQuery(context.Background(), "/key", k1)
	require.NoError(t, err)
	q2, err := batch.ABCIQuery(context.Background(), "/key", k2)
	require.NoError(t, err)
	require.Equal(t, 2, batch.Count())
	qresults, err := batch.Send(ctx)
	require.NoError(t, err)
	require.Len(t, qresults, 2)
	require.Equal(t, 0, batch.Count())

	qresult1, ok := qresults[0].(*ctypes.ResultABCIQuery)
	require.True(t, ok)
	require.Equal(t, *qresult1, *q1)
	qresult2, ok := qresults[1].(*ctypes.ResultABCIQuery)
	require.True(t, ok)
	require.Equal(t, *qresult2, *q2)

	require.Equal(t, qresult1.Response.Key, k1)
	require.Equal(t, qresult2.Response.Key, k2)
	require.Equal(t, qresult1.Response.Value, v1)
	require.Equal(t, qresult2.Response.Value, v2)
}

func TestSendingEmptyRequestBatch(t *testing.T) {
	c := getHTTPClient()
	batch := c.NewBatch()
	_, err := batch.Send(ctx)
	require.Error(t, err, "sending an empty batch of JSON RPC requests should result in an error")
}

func TestClearingEmptyRequestBatch(t *testing.T) {
	c := getHTTPClient()
	batch := c.NewBatch()
	require.Zero(t, batch.Clear(), "clearing an empty batch of JSON RPC requests should result in a 0 result")
}

func TestConcurrentJSONRPCBatching(t *testing.T) {
	var wg sync.WaitGroup
	c := getHTTPClient()
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			testBatchedJSONRPCCalls(t, c)
		}()
	}
	wg.Wait()
}
