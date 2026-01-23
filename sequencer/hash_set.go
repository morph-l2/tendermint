package sequencer

import (
	"sync"

	"github.com/morph-l2/go-ethereum/common"
)

// HashSet is a fixed-capacity set for common.Hash with LRU eviction.
type HashSet struct {
	items    []common.Hash
	index    map[common.Hash]int
	capacity int
	head     int
	count    int

	mtx sync.RWMutex
}

// NewHashSet creates a new hash set.
func NewHashSet(capacity int) *HashSet {
	return &HashSet{
		items:    make([]common.Hash, capacity),
		index:    make(map[common.Hash]int, capacity),
		capacity: capacity,
	}
}

// Add adds a hash. Returns true if already existed.
func (s *HashSet) Add(hash common.Hash) bool {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	if _, exists := s.index[hash]; exists {
		return true
	}

	// Evict old if full
	if s.count == s.capacity {
		old := s.items[s.head]
		delete(s.index, old)
	}

	s.items[s.head] = hash
	s.index[hash] = s.head
	s.head = (s.head + 1) % s.capacity
	if s.count < s.capacity {
		s.count++
	}

	return false
}

// Contains checks if hash exists.
func (s *HashSet) Contains(hash common.Hash) bool {
	s.mtx.RLock()
	defer s.mtx.RUnlock()
	_, exists := s.index[hash]
	return exists
}

// PeerHashSet is a per-peer hash set.
type PeerHashSet struct {
	peers      map[string]*HashSet
	perPeerCap int
	mtx        sync.RWMutex
}

// NewPeerHashSet creates a new per-peer hash set.
func NewPeerHashSet(perPeerCapacity int) *PeerHashSet {
	return &PeerHashSet{
		peers:      make(map[string]*HashSet),
		perPeerCap: perPeerCapacity,
	}
}

// Add marks a hash for a peer.
func (s *PeerHashSet) Add(peerID string, hash common.Hash) {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	set, ok := s.peers[peerID]
	if !ok {
		set = NewHashSet(s.perPeerCap)
		s.peers[peerID] = set
	}
	set.Add(hash)
}

// Contains checks if hash exists for peer.
func (s *PeerHashSet) Contains(peerID string, hash common.Hash) bool {
	s.mtx.RLock()
	set, ok := s.peers[peerID]
	s.mtx.RUnlock()

	if !ok {
		return false
	}
	return set.Contains(hash)
}

// AddPeer initializes tracking for a peer.
func (s *PeerHashSet) AddPeer(peerID string) {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	if _, ok := s.peers[peerID]; !ok {
		s.peers[peerID] = NewHashSet(s.perPeerCap)
	}
}

// RemovePeer removes a peer.
func (s *PeerHashSet) RemovePeer(peerID string) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	delete(s.peers, peerID)
}
