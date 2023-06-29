package types

import (
	"bytes"
	"testing"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/assert"

	tmproto "github.com/tendermint/tendermint/proto/tendermint/types"
)

func TestWriteReadMessageSimple(t *testing.T) {
	cases := []proto.Message{
		&RequestEcho{
			Message: "Hello",
		},
	}

	for _, c := range cases {
		buf := new(bytes.Buffer)
		err := WriteMessage(c, buf)
		assert.Nil(t, err)

		msg := new(RequestEcho)
		err = ReadMessage(buf, msg)
		assert.Nil(t, err)

		assert.True(t, proto.Equal(c, msg))
	}
}

func TestWriteReadMessage(t *testing.T) {
	cases := []proto.Message{
		&tmproto.Header{
			Height:  4,
			ChainID: "test",
		},
		// TODO: add the rest
	}

	for _, c := range cases {
		buf := new(bytes.Buffer)
		err := WriteMessage(c, buf)
		assert.Nil(t, err)

		msg := new(tmproto.Header)
		err = ReadMessage(buf, msg)
		assert.Nil(t, err)

		assert.True(t, proto.Equal(c, msg))
	}
}
