package sequencer

import (
	"sync"
	"time"

	"github.com/tendermint/tendermint/p2p"
)

// tokenBucket is the per-peer token bucket state.
// Tokens are refilled lazily on each Allow() call based on elapsed time.
type tokenBucket struct {
	tokens     float64
	lastRefill time.Time
}

// PeerRateLimiter enforces a per-peer token-bucket rate limit.
//
// Each peer has its own bucket with capacity `burst`, refilled at `rate`
// tokens per second. Allow() consumes one token and returns false when the
// bucket is empty.
//
// The limiter is safe for concurrent use. Buckets are created on AddPeer
// (initialized full, so newly connected peers are not penalized) and
// removed on RemovePeer. Allow() on an unknown peer lazily creates a full
// bucket as a fallback.
type PeerRateLimiter struct {
	rate  float64 // tokens per second
	burst float64 // bucket capacity

	mu      sync.Mutex
	buckets map[p2p.ID]*tokenBucket
}

// NewPeerRateLimiter creates a limiter with the given rate (tokens/sec) and
// burst (max tokens in a bucket).
func NewPeerRateLimiter(rate float64, burst int) *PeerRateLimiter {
	return &PeerRateLimiter{
		rate:    rate,
		burst:   float64(burst),
		buckets: make(map[p2p.ID]*tokenBucket),
	}
}

// AddPeer initializes a full bucket for a newly connected peer.
// Calling AddPeer for a peer that already has a bucket is a no-op.
func (l *PeerRateLimiter) AddPeer(peerID p2p.ID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.buckets[peerID]; ok {
		return
	}
	l.buckets[peerID] = &tokenBucket{
		tokens:     l.burst,
		lastRefill: time.Now(),
	}
}

// RemovePeer drops the bucket for a disconnected peer.
func (l *PeerRateLimiter) RemovePeer(peerID p2p.ID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, peerID)
}

// Allow returns true if the peer is allowed to perform one action, consuming
// one token. Returns false if the peer has exhausted its bucket.
//
// If the peer has no bucket (e.g. AddPeer was not called), a full bucket is
// created as a safe fallback.
func (l *PeerRateLimiter) Allow(peerID p2p.ID) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	bucket, ok := l.buckets[peerID]
	if !ok {
		bucket = &tokenBucket{tokens: l.burst, lastRefill: time.Now()}
		l.buckets[peerID] = bucket
	}

	now := time.Now()
	elapsed := now.Sub(bucket.lastRefill).Seconds()
	bucket.tokens += elapsed * l.rate
	if bucket.tokens > l.burst {
		bucket.tokens = l.burst
	}
	bucket.lastRefill = now

	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens--
	return true
}
