package mock

import (
	"context"

	abci "github.com/tendermint/tendermint/abci/types"
	"github.com/tendermint/tendermint/libs/bytes"
	"github.com/tendermint/tendermint/proxy"
	"github.com/tendermint/tendermint/rpc/client"
	ctypes "github.com/tendermint/tendermint/rpc/core/types"
)

// ABCIApp will send all abci related request to the named app,
// so you can test app behavior from a client without needing
// an entire tendermint node
type ABCIApp struct {
	App abci.Application
}

var (
	_ client.ABCIClient = ABCIApp{}
	_ client.ABCIClient = ABCIMock{}
	_ client.ABCIClient = (*ABCIRecorder)(nil)
)

func (a ABCIApp) ABCIInfo(ctx context.Context) (*ctypes.ResultABCIInfo, error) {
	return &ctypes.ResultABCIInfo{Response: a.App.Info(proxy.RequestInfo)}, nil
}

func (a ABCIApp) ABCIQuery(ctx context.Context, path string, data bytes.HexBytes) (*ctypes.ResultABCIQuery, error) {
	return a.ABCIQueryWithOptions(ctx, path, data, client.DefaultABCIQueryOptions)
}

func (a ABCIApp) ABCIQueryWithOptions(
	ctx context.Context,
	path string,
	data bytes.HexBytes,
	opts client.ABCIQueryOptions) (*ctypes.ResultABCIQuery, error) {
	q := a.App.Query(abci.RequestQuery{
		Data:   data,
		Path:   path,
		Height: opts.Height,
		Prove:  opts.Prove,
	})
	return &ctypes.ResultABCIQuery{Response: q}, nil
}

// ABCIMock will send all abci related request to the named app,
// so you can test app behavior from a client without needing
// an entire tendermint node
type ABCIMock struct {
	Info      Call
	Query     Call
	Broadcast Call
}

func (m ABCIMock) ABCIInfo(ctx context.Context) (*ctypes.ResultABCIInfo, error) {
	res, err := m.Info.GetResponse(nil)
	if err != nil {
		return nil, err
	}
	return &ctypes.ResultABCIInfo{Response: res.(abci.ResponseInfo)}, nil
}

func (m ABCIMock) ABCIQuery(ctx context.Context, path string, data bytes.HexBytes) (*ctypes.ResultABCIQuery, error) {
	return m.ABCIQueryWithOptions(ctx, path, data, client.DefaultABCIQueryOptions)
}

func (m ABCIMock) ABCIQueryWithOptions(
	ctx context.Context,
	path string,
	data bytes.HexBytes,
	opts client.ABCIQueryOptions) (*ctypes.ResultABCIQuery, error) {
	res, err := m.Query.GetResponse(QueryArgs{path, data, opts.Height, opts.Prove})
	if err != nil {
		return nil, err
	}
	resQuery := res.(abci.ResponseQuery)
	return &ctypes.ResultABCIQuery{Response: resQuery}, nil
}

// ABCIRecorder can wrap another type (ABCIApp, ABCIMock, or Client)
// and record all ABCI related calls.
type ABCIRecorder struct {
	Client client.ABCIClient
	Calls  []Call
}

func NewABCIRecorder(client client.ABCIClient) *ABCIRecorder {
	return &ABCIRecorder{
		Client: client,
		Calls:  []Call{},
	}
}

type QueryArgs struct {
	Path   string
	Data   bytes.HexBytes
	Height int64
	Prove  bool
}

func (r *ABCIRecorder) addCall(call Call) {
	r.Calls = append(r.Calls, call)
}

func (r *ABCIRecorder) ABCIInfo(ctx context.Context) (*ctypes.ResultABCIInfo, error) {
	res, err := r.Client.ABCIInfo(ctx)
	r.addCall(Call{
		Name:     "abci_info",
		Response: res,
		Error:    err,
	})
	return res, err
}

func (r *ABCIRecorder) ABCIQuery(
	ctx context.Context,
	path string,
	data bytes.HexBytes,
) (*ctypes.ResultABCIQuery, error) {
	return r.ABCIQueryWithOptions(ctx, path, data, client.DefaultABCIQueryOptions)
}

func (r *ABCIRecorder) ABCIQueryWithOptions(
	ctx context.Context,
	path string,
	data bytes.HexBytes,
	opts client.ABCIQueryOptions) (*ctypes.ResultABCIQuery, error) {
	res, err := r.Client.ABCIQueryWithOptions(ctx, path, data, opts)
	r.addCall(Call{
		Name:     "abci_query",
		Args:     QueryArgs{path, data, opts.Height, opts.Prove},
		Response: res,
		Error:    err,
	})
	return res, err
}
