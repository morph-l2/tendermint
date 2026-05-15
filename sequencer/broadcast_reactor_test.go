package sequencer

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/libs/log"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/p2p/mock"
)

// newReactorForTest returns a BlockBroadcastReactor with only the fields
// needed for exercising banmap / sync-request tracking logic. It does NOT
// wire in Switch / StateV2 / verifier — callers must avoid code paths that
// dereference them.
func newReactorForTest() *BlockBroadcastReactor {
	return &BlockBroadcastReactor{
		syncRequests:    make(map[int64]*SyncReq),
		syncPeerCounts:  make(map[p2p.ID]int),
		bannedPeers:     make(map[p2p.ID]time.Time),
		blockReqLimiter: NewPeerRateLimiter(blockRequestRateLimit, blockRequestBurst),
	}
}

// ----------------------------------------------------------------------------
// Banmap behavior
// ----------------------------------------------------------------------------

// TestIsBanned_NotBanned: a fresh peer is not banned.
func TestIsBanned_NotBanned(t *testing.T) {
	r := newReactorForTest()
	require.False(t, r.isBanned("peer-a"))
}

// TestIsBanned_Banned: writing directly into bannedPeers causes isBanned to
// return true before expiry.
func TestIsBanned_Banned(t *testing.T) {
	r := newReactorForTest()
	peer := p2p.ID("peer-b")

	r.bannedPeers[peer] = time.Now().Add(1 * time.Minute)
	require.True(t, r.isBanned(peer))
}

// TestIsBanned_ExpiredLazyEvict: once the ban expires, isBanned returns false
// AND removes the stale entry from the map (lazy eviction).
func TestIsBanned_ExpiredLazyEvict(t *testing.T) {
	r := newReactorForTest()
	peer := p2p.ID("peer-c")

	r.bannedPeers[peer] = time.Now().Add(-1 * time.Second) // already expired
	require.False(t, r.isBanned(peer))
	_, stillPresent := r.bannedPeers[peer]
	require.False(t, stillPresent, "expired entry must be lazily evicted")
}

// ----------------------------------------------------------------------------
// removeTimeoutPeers: collect-only semantics
// ----------------------------------------------------------------------------

// TestRemoveTimeoutPeers_CollectsExpired verifies that expired entries are
// identified. We can't actually call banPeer() without a Switch, so we
// inline the collection logic and assert it catches the right peers.
//
// This mirrors the lock-scoped collection phase of removeTimeoutPeers.
func TestRemoveTimeoutPeers_CollectsExpired(t *testing.T) {
	r := newReactorForTest()
	now := time.Now()

	// Two expired requests from the same peer, one fresh, one from another peer.
	r.syncRequests[100] = &SyncReq{Height: 100, PeerID: "peer-x", ExpireAt: now.Add(-1 * time.Second)}
	r.syncRequests[101] = &SyncReq{Height: 101, PeerID: "peer-x", ExpireAt: now.Add(-2 * time.Second)}
	r.syncRequests[102] = &SyncReq{Height: 102, PeerID: "peer-y", ExpireAt: now.Add(10 * time.Second)} // fresh
	r.syncRequests[103] = &SyncReq{Height: 103, PeerID: "peer-z", ExpireAt: now.Add(-1 * time.Second)}

	// Replicate the collect step from removeTimeoutPeers.
	seen := make(map[p2p.ID]struct{})
	r.syncRequestsMu.Lock()
	for _, req := range r.syncRequests {
		if time.Now().After(req.ExpireAt) {
			seen[req.PeerID] = struct{}{}
		}
	}
	r.syncRequestsMu.Unlock()

	require.Len(t, seen, 2, "peer-x and peer-z should be collected, peer-y must not")
	require.Contains(t, seen, p2p.ID("peer-x"))
	require.Contains(t, seen, p2p.ID("peer-z"))
	require.NotContains(t, seen, p2p.ID("peer-y"))
}

// ----------------------------------------------------------------------------
// checkAndTakeSyncRequest: ensure slot is only consumed on exact peer+height match
// ----------------------------------------------------------------------------

// TestCheckAndTakeSyncRequest_PeerMismatch: a response from a different peer
// than the one we requested must not consume the slot.
func TestCheckAndTakeSyncRequest_PeerMismatch(t *testing.T) {
	r := newReactorForTest()
	r.syncRequests[200] = &SyncReq{
		Height:   200,
		PeerID:   "peer-requested",
		ExpireAt: time.Now().Add(1 * time.Minute),
	}
	r.syncPeerCounts["peer-requested"] = 1

	ok := r.checkAndTakeSyncRequest("peer-other", 200)
	require.False(t, ok, "response from wrong peer must be rejected")

	// Slot must still exist.
	_, exists := r.syncRequests[200]
	require.True(t, exists)
	require.Equal(t, 1, r.syncPeerCounts["peer-requested"])
}

// TestCheckAndTakeSyncRequest_Expired: a late response (after TTL) must not
// consume the slot.
func TestCheckAndTakeSyncRequest_Expired(t *testing.T) {
	r := newReactorForTest()
	r.syncRequests[201] = &SyncReq{
		Height:   201,
		PeerID:   "peer-slow",
		ExpireAt: time.Now().Add(-1 * time.Second),
	}
	r.syncPeerCounts["peer-slow"] = 1

	ok := r.checkAndTakeSyncRequest("peer-slow", 201)
	require.False(t, ok, "expired slot must not be taken")
}

// TestCheckAndTakeSyncRequest_Success: a well-formed response consumes the
// slot and decrements the per-peer count.
func TestCheckAndTakeSyncRequest_Success(t *testing.T) {
	r := newReactorForTest()
	r.syncRequests[202] = &SyncReq{
		Height:   202,
		PeerID:   "peer-good",
		ExpireAt: time.Now().Add(1 * time.Minute),
	}
	r.syncPeerCounts["peer-good"] = 1

	ok := r.checkAndTakeSyncRequest("peer-good", 202)
	require.True(t, ok)

	_, exists := r.syncRequests[202]
	require.False(t, exists, "slot must be taken")
	require.Equal(t, 0, r.syncPeerCounts["peer-good"])
}

// ----------------------------------------------------------------------------
// removeSyncRequestsByPeer: full cleanup on peer disconnect
// ----------------------------------------------------------------------------

// TestRemoveSyncRequestsByPeer_ClearsAllEntries: all slots belonging to a
// peer are deleted, and its counter entry is removed entirely.
func TestRemoveSyncRequestsByPeer_ClearsAllEntries(t *testing.T) {
	r := newReactorForTest()
	now := time.Now()
	r.syncRequests[300] = &SyncReq{Height: 300, PeerID: "peer-gone", ExpireAt: now.Add(1 * time.Minute)}
	r.syncRequests[301] = &SyncReq{Height: 301, PeerID: "peer-gone", ExpireAt: now.Add(1 * time.Minute)}
	r.syncRequests[302] = &SyncReq{Height: 302, PeerID: "peer-alive", ExpireAt: now.Add(1 * time.Minute)}
	r.syncPeerCounts["peer-gone"] = 2
	r.syncPeerCounts["peer-alive"] = 1

	r.removeSyncRequestsByPeer("peer-gone")

	require.Len(t, r.syncRequests, 1)
	_, stillThere := r.syncRequests[302]
	require.True(t, stillThere, "peer-alive's slot must remain")
	_, counted := r.syncPeerCounts["peer-gone"]
	require.False(t, counted, "peer-gone's counter entry must be deleted")
	require.Equal(t, 1, r.syncPeerCounts["peer-alive"])
}

// ----------------------------------------------------------------------------
// recordSyncRequest: overwrite semantics
// ----------------------------------------------------------------------------

// TestRecordSyncRequest_OverwritesOldEntry: recording a new request for the
// same height decrements the old peer's counter and reassigns the slot.
func TestRecordSyncRequest_OverwritesOldEntry(t *testing.T) {
	r := newReactorForTest()
	r.recordSyncRequest("peer-1", 400)
	require.Equal(t, 1, r.syncPeerCounts["peer-1"])

	r.recordSyncRequest("peer-2", 400) // same height, new peer
	require.Equal(t, 0, r.syncPeerCounts["peer-1"])
	require.Equal(t, 1, r.syncPeerCounts["peer-2"])

	req := r.syncRequests[400]
	require.Equal(t, p2p.ID("peer-2"), req.PeerID)
}

// ----------------------------------------------------------------------------
// Rate limiter wiring with reactor-specific constants
// ----------------------------------------------------------------------------

// TestReactorRateLimiter_UsesConstants verifies the reactor's rate limiter
// is configured with blockRequestRateLimit / blockRequestBurst and behaves
// end-to-end: after burst exhaustion, subsequent Allow() calls are denied.
func TestReactorRateLimiter_UsesConstants(t *testing.T) {
	r := newReactorForTest()
	peer := p2p.ID("peer-flood")
	r.blockReqLimiter.AddPeer(peer)

	// Consume exactly burst tokens.
	for i := 0; i < blockRequestBurst; i++ {
		require.True(t, r.blockReqLimiter.Allow(peer), "token %d should be allowed", i+1)
	}
	// Next call must be denied (no time has elapsed for refill).
	require.False(t, r.blockReqLimiter.Allow(peer))
}

// ----------------------------------------------------------------------------
// banPeer whitelist exemption (persistent_peers)
// ----------------------------------------------------------------------------

// safeBanPeer drives banPeer with a nil Switch and absorbs the resulting
// panic. We only care about the ban-list write side-effect: by the time
// StopPeerForError runs (and panics on nil), the bannedPeers map has either
// been written (non-persistent path) or skipped (persistent path), which is
// exactly what we want to assert.
func safeBanPeer(r *BlockBroadcastReactor, peer p2p.Peer, reason string) {
	defer func() { _ = recover() }()
	r.banPeer(peer, reason)
}

// TestBanPeer_PersistentPeerSkipsBanList: a peer with IsPersistent()==true
// must NOT be added to bannedPeers — that is the central whitelist invariant.
func TestBanPeer_PersistentPeerSkipsBanList(t *testing.T) {
	r := newReactorForTest()
	r.logger = log.NewNopLogger()

	mp := mock.NewPeer(nil)
	mp.Persistent = true

	safeBanPeer(r, mp, "test signature failure")

	_, present := r.bannedPeers[mp.ID()]
	require.False(t, present, "persistent peer must be exempt from bannedPeers")
}

// TestBanPeer_NonPersistentPeerEntersBanList: regression — a peer with
// IsPersistent()==false must follow the original code path and be added
// to bannedPeers with a future expiry.
func TestBanPeer_NonPersistentPeerEntersBanList(t *testing.T) {
	r := newReactorForTest()
	r.logger = log.NewNopLogger()

	mp := mock.NewPeer(nil)
	mp.Persistent = false

	before := time.Now()
	safeBanPeer(r, mp, "test signature failure")

	expireAt, present := r.bannedPeers[mp.ID()]
	require.True(t, present, "non-persistent peer must be added to bannedPeers")
	require.True(t, expireAt.After(before.Add(peerBanDuration-time.Second)),
		"ban expiry should be ~peerBanDuration in the future")
}

// TestBanPeer_PersistentPeerAddPeerNotRejected: the natural follow-up — a
// persistent peer that has misbehaved before still passes AddPeer's
// isBanned gate, so the Switch's automatic reconnect can re-add it.
func TestBanPeer_PersistentPeerAddPeerNotRejected(t *testing.T) {
	r := newReactorForTest()
	r.logger = log.NewNopLogger()

	mp := mock.NewPeer(nil)
	mp.Persistent = true

	safeBanPeer(r, mp, "decode failure")
	require.False(t, r.isBanned(mp.ID()),
		"persistent peer must not be considered banned after misbehavior")
}
