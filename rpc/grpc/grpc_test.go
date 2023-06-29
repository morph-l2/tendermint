package coregrpc_test

import (
	"os"
	"testing"

	"github.com/tendermint/tendermint/abci/example/kvstore"
	rpctest "github.com/tendermint/tendermint/rpc/test"
)

func TestMain(m *testing.M) {
	// start a tendermint node in the background to test against
	app := kvstore.NewApplication()
	node := rpctest.StartTendermint(app)

	code := m.Run()

	// and shut down proper at the end
	rpctest.StopTendermint(node)
	os.Exit(code)
}
