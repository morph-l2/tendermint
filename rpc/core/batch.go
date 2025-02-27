package core

import (
	"errors"

	eth "github.com/morph-l2/go-ethereum/core/types"
	rpctypes "github.com/tendermint/tendermint/rpc/jsonrpc/types"
)

// BatchByIndex(index uint64) (*eth.RollupBatch, []*eth.BatchSignature, error)
func BatchByIndex(ctx *rpctypes.Context, index uint64) (*eth.RollupBatch, []*eth.BatchSignature, error) {
	if env == nil {
		return nil, nil, errors.New("env is nil")
	}
	if env.L2Node == nil {
		return nil, nil, errors.New("env.L2Node is nil")
	}

	return env.L2Node.BatchByIndex(index)
}
