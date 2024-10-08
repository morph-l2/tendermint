package client_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tmrand "github.com/tendermint/tendermint/libs/rand"
	"github.com/tendermint/tendermint/rpc/client"
	"github.com/tendermint/tendermint/types"
)

var waitForEventTimeout = 8 * time.Second

// MakeTxKV returns a text transaction, allong with expected key, value pair
func MakeTxKV() ([]byte, []byte, []byte) {
	k := []byte(tmrand.Str(8))
	v := []byte(tmrand.Str(8))
	return k, v, append(k, append([]byte("="), v...)...)
}

func TestHeaderEvents(t *testing.T) {
	for i, c := range GetClients() {
		i, c := i, c
		t.Run(reflect.TypeOf(c).String(), func(t *testing.T) {
			// start for this test it if it wasn't already running
			if !c.IsRunning() {
				// if so, then we start it, listen, and stop it.
				err := c.Start()
				require.Nil(t, err, "%d: %+v", i, err)
				t.Cleanup(func() {
					if err := c.Stop(); err != nil {
						t.Error(err)
					}
				})
			}

			evtTyp := types.EventNewBlockHeader
			evt, err := client.WaitForOneEvent(c, evtTyp, waitForEventTimeout)
			require.Nil(t, err, "%d: %+v", i, err)
			_, ok := evt.(types.EventDataNewBlockHeader)
			require.True(t, ok, "%d: %#v", i, evt)
			// TODO: more checks...
		})
	}
}

// subscribe to new blocks and make sure height increments by 1
func TestBlockEvents(t *testing.T) {
	for _, c := range GetClients() {
		c := c
		t.Run(reflect.TypeOf(c).String(), func(t *testing.T) {

			// start for this test it if it wasn't already running
			if !c.IsRunning() {
				// if so, then we start it, listen, and stop it.
				err := c.Start()
				require.Nil(t, err)
				t.Cleanup(func() {
					if err := c.Stop(); err != nil {
						t.Error(err)
					}
				})
			}

			const subscriber = "TestBlockEvents"

			eventCh, err := c.Subscribe(context.Background(), subscriber, types.QueryForEvent(types.EventNewBlock).String())
			require.NoError(t, err)
			t.Cleanup(func() {
				if err := c.UnsubscribeAll(context.Background(), subscriber); err != nil {
					t.Error(err)
				}
			})

			var firstBlockHeight int64
			for i := int64(0); i < 3; i++ {
				event := <-eventCh
				blockEvent, ok := event.Data.(types.EventDataNewBlock)
				require.True(t, ok)

				block := blockEvent.Block

				if firstBlockHeight == 0 {
					firstBlockHeight = block.Header.Height
				}

				require.Equal(t, firstBlockHeight+i, block.Header.Height)
			}
		})
	}
}

// Test HTTPClient resubscribes upon disconnect && subscription error.
// Test Local client resubscribes upon subscription error.
func TestClientsResubscribe(t *testing.T) {
	// TODO(melekes)
}

func TestHTTPReturnsErrorIfClientIsNotRunning(t *testing.T) {
	c := getHTTPClient()

	// on Subscribe
	_, err := c.Subscribe(context.Background(), "TestHeaderEvents",
		types.QueryForEvent(types.EventNewBlockHeader).String())
	assert.Error(t, err)

	// on Unsubscribe
	err = c.Unsubscribe(context.Background(), "TestHeaderEvents",
		types.QueryForEvent(types.EventNewBlockHeader).String())
	assert.Error(t, err)

	// on UnsubscribeAll
	err = c.UnsubscribeAll(context.Background(), "TestHeaderEvents")
	assert.Error(t, err)
}
