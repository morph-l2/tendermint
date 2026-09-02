package sequencer

import (
	"errors"

	"github.com/morph-l2/go-ethereum/common"
)

// Sentinel errors for block processing
var (
	// ErrInvalidSignature indicates the block's signature is provably invalid.
	// This is a malicious-peer signal: the block's content does not match a
	// legitimate sequencer signature. Callers should ban the sender.
	ErrInvalidSignature = errors.New("invalid block signature")

	// ErrVerifierUnavailable indicates the local verifier could not determine
	// whether a signature is valid (e.g. not configured, L1 backend RPC
	// failure, transient IO error). This is NOT a peer-misbehavior signal —
	// the peer may be honest; we just cannot decide right now. Callers must
	// NOT ban the sender on this error; retry or drop silently instead.
	ErrVerifierUnavailable = errors.New("verifier unavailable")
)

// SequencerVerifier verifies if an address is a valid L1 sequencer.
type SequencerVerifier interface {
	// IsSequencerAt checks if addr was the valid sequencer at the given L2 block height.
	IsSequencerAt(addr common.Address, l2Height uint64) (bool, error)
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
	// Start initializes and starts the Raft service.
	// leader (bootstrap=true): starts as a single-node cluster, immediately becomes Leader.
	// follower (bootstrap=false): initializes Raft, then asynchronously retries Join() in the
	//   background until it successfully joins the cluster or the service is stopped.
	// Called by StateV2.OnStart() when the upgrade height is reached.
	Start() error

	// Stop gracefully shuts down the Raft service.
	// Called by StateV2.OnStop() when the upgrade height has been passed.
	Stop()

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

	// SetOnBlockApplied registers the callback invoked by the Raft FSM on every
	// committed log entry. The callback should execute ApplyBlock + SaveSignature.
	// Must be called before Start().
	SetOnBlockApplied(fn func(*BlockV2) error)

	TransferLeader() error
}

// L1Tracker reports whether L1 RPC has fallen too far behind for this node to
// safely act. When L1 is stale, the node may be blind to L1 SequencerUpdated
// events, so the sequencer must stop producing and fullnodes must stop syncing
// to avoid following a revoked sequencer. Implemented by the L1 tracker in the
// node binary and required by StateV2 / the broadcast reactor.
type L1Tracker interface {
	// IsHalt reports whether L1 is stale enough that this node must halt block
	// production and sync.
	IsHalt() bool
}
