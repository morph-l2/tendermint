package sequencer

import (
	"errors"

	"github.com/morph-l2/go-ethereum/common"
)

// Sentinel errors for block processing
var (
	// ErrInvalidSignature indicates block signature verification failed
	ErrInvalidSignature = errors.New("invalid block signature")
)

// SequencerVerifier verifies if an address is a valid L1 sequencer.
type SequencerVerifier interface {
	// IsSequencerAt checks if addr was the valid sequencer at the given L2 block height.
	IsSequencerAt(addr common.Address, l2Height uint64) (bool, error)

	// VerificationStartHeight returns the L2 block height from which V2 signature
	// verification is enforced (= upgradeBlockHeight). Blocks below this height are
	// PBFT blocks and skip V2 verification. Returns math.MaxUint64 if not configured.
	VerificationStartHeight() uint64
}

// Signer interface for sequencer block signing
type Signer interface {
	// Sign signs the data with the sequencer's private key
	Sign(data []byte) ([]byte, error)
	// Address returns the sequencer's address
	Address() common.Address
}

// SequencerHA is the abstraction for Raft HA cluster.
// In single-node mode, ha == nil and all HA-related logic is skipped.
type SequencerHA interface {
	// IsLeader returns whether the current node is the Raft leader (sole block producer).
	IsLeader() bool

	// Join adds this node to the Raft cluster.
	// Precondition: node has synced to near chain tip via Fullnode mode.
	// Fails if localHeight < raft.EarliestRetainedLogHeight.
	// On success, the node is a full Raft member and P2P sync can be stopped.
	Join() error

	// Commit replicates a signed block via Raft to the cluster.
	// Blocks until majority of nodes have acknowledged receipt (NOT applied).
	// ApplyBlock is handled separately by leader (after Commit) and followers (via Subscribe).
	// On failure, the caller should abandon this block production round.
	Commit(block *BlockV2) error

	// Subscribe returns a channel that delivers blocks after Raft commit.
	// Both leader and follower subscribe; used by broadcastRoutine for P2P broadcast.
	Subscribe() <-chan *BlockV2
}
