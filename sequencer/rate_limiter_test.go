package sequencer

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/p2p"
)

const (
	testRate  = 10.0 // tokens/sec
	testBurst = 5    // bucket capacity
)

func newTestLimiter() *PeerRateLimiter {
	return NewPeerRateLimiter(testRate, testBurst)
}

// TestAllow_WithinBurst verifies that a peer can fire up to burst requests
// immediately after connecting (bucket starts full).
func TestAllow_WithinBurst(t *testing.T) {
	l := newTestLimiter()
	peer := p2p.ID("peer-a")
	l.AddPeer(peer)

	for i := 0; i < testBurst; i++ {
		require.True(t, l.Allow(peer), "request %d should be allowed (within burst)", i+1)
	}
}

// TestAllow_ExceedBurst verifies that the (burst+1)-th immediate request is denied.
func TestAllow_ExceedBurst(t *testing.T) {
	l := newTestLimiter()
	peer := p2p.ID("peer-b")
	l.AddPeer(peer)

	for i := 0; i < testBurst; i++ {
		l.Allow(peer)
	}
	require.False(t, l.Allow(peer), "request beyond burst should be denied")
}

// TestAllow_RefillAfterDelay verifies tokens are replenished over time.
func TestAllow_RefillAfterDelay(t *testing.T) {
	l := newTestLimiter()
	peer := p2p.ID("peer-c")
	l.AddPeer(peer)

	// Drain the bucket completely.
	for i := 0; i < testBurst; i++ {
		l.Allow(peer)
	}
	require.False(t, l.Allow(peer), "bucket should be empty")

	// Wait for 1 token to refill (rate=10/s → 100ms per token).
	time.Sleep(120 * time.Millisecond)
	require.True(t, l.Allow(peer), "one token should have been refilled")
}

// TestAllow_UnknownPeer verifies that a peer not added via AddPeer gets a
// full-bucket fallback and is not permanently blocked.
func TestAllow_UnknownPeer(t *testing.T) {
	l := newTestLimiter()
	peer := p2p.ID("peer-unknown")
	// No AddPeer call — Allow should create a full bucket as fallback.
	require.True(t, l.Allow(peer), "unknown peer should get a full bucket fallback")
}

// TestAddPeer_Idempotent verifies that calling AddPeer twice does not reset
// an existing bucket (tokens already consumed should stay consumed).
func TestAddPeer_Idempotent(t *testing.T) {
	l := newTestLimiter()
	peer := p2p.ID("peer-d")
	l.AddPeer(peer)

	// Drain the bucket.
	for i := 0; i < testBurst; i++ {
		l.Allow(peer)
	}
	require.False(t, l.Allow(peer))

	// Second AddPeer must not reset the bucket.
	l.AddPeer(peer)
	require.False(t, l.Allow(peer), "AddPeer must not reset an existing bucket")
}

// TestRemovePeer_CleansUp verifies that after RemovePeer the bucket is gone
// and a subsequent Allow re-creates a fresh full bucket (fallback path).
func TestRemovePeer_CleansUp(t *testing.T) {
	l := newTestLimiter()
	peer := p2p.ID("peer-e")
	l.AddPeer(peer)

	// Drain the bucket.
	for i := 0; i < testBurst; i++ {
		l.Allow(peer)
	}
	require.False(t, l.Allow(peer))

	// Remove peer — bucket must be deleted.
	l.RemovePeer(peer)

	// Next Allow triggers the unknown-peer fallback (full bucket).
	require.True(t, l.Allow(peer), "after RemovePeer, Allow should create a fresh full bucket")
}

// TestAllow_MultiplePeers verifies that buckets are independent per peer.
func TestAllow_MultiplePeers(t *testing.T) {
	l := newTestLimiter()
	peerA := p2p.ID("peer-f")
	peerB := p2p.ID("peer-g")
	l.AddPeer(peerA)
	l.AddPeer(peerB)

	// Drain peerA entirely.
	for i := 0; i < testBurst; i++ {
		l.Allow(peerA)
	}
	require.False(t, l.Allow(peerA), "peerA should be exhausted")

	// peerB's bucket must be untouched.
	require.True(t, l.Allow(peerB), "peerB should still have tokens")
}

// TestAllow_BurstCapNotExceeded verifies that tokens never exceed burst even
// after a long idle period.
func TestAllow_BurstCapNotExceeded(t *testing.T) {
	l := newTestLimiter()
	peer := p2p.ID("peer-h")
	l.AddPeer(peer)

	// Drain fully.
	for i := 0; i < testBurst; i++ {
		l.Allow(peer)
	}

	// Wait long enough for many tokens to theoretically accumulate.
	time.Sleep(500 * time.Millisecond) // would add 5 tokens at rate=10

	// Can only consume up to burst, not more.
	allowed := 0
	for l.Allow(peer) {
		allowed++
	}
	require.LessOrEqual(t, allowed, testBurst, "tokens must not exceed burst capacity")
}
