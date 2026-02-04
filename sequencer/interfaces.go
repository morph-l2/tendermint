package sequencer

import (
	"context"

	"github.com/morph-l2/go-ethereum/common"
)

// SequencerVerifier verifies if an address is the current L1 sequencer
type SequencerVerifier interface {
	IsSequencer(ctx context.Context, addr common.Address) (bool, error)
}

// Signer interface for sequencer block signing
type Signer interface {
	// Sign signs the data with the sequencer's private key
	Sign(data []byte) ([]byte, error)
	// Address returns the sequencer's address
	Address() common.Address
	// IsActiveSequencer checks if this signer is the current L1 sequencer
	IsActiveSequencer(ctx context.Context) (bool, error)
}
