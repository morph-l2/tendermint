package blocksync

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tendermint/tendermint/libs/log"
	tmrand "github.com/tendermint/tendermint/libs/rand"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/types"
)

func init() {
	peerTimeout = 2 * time.Second
}

type testPeer struct {
	id        p2p.ID
	base      int64
	height    int64
	inputChan chan inputData // make sure each peer's data is sequential
}

type inputData struct {
	t       *testing.T
	pool    *BlockPool
	request BlockRequest
}

func (p testPeer) runInputRoutine() {
	go func() {
		for input := range p.inputChan {
			p.simulateInput(input)
		}
	}()
}

// Request desired, pretend like we got the block immediately.
func (p testPeer) simulateInput(input inputData) {
	block := &types.Block{Header: types.Header{Height: input.request.Height}}
	input.pool.AddBlock(input.request.PeerID, block, 123)
	// TODO: uncommenting this creates a race which is detected by:
	// https://github.com/golang/go/blob/2bd767b1022dd3254bcec469f0ee164024726486/src/testing/testing.go#L854-L856
	// see: https://github.com/tendermint/tendermint/issues/3390#issue-418379890
	// input.t.Logf("Added block from peer %v (height: %v)", input.request.PeerID, input.request.Height)
}

type testPeers map[p2p.ID]testPeer

func (ps testPeers) start() {
	for _, v := range ps {
		v.runInputRoutine()
	}
}

func (ps testPeers) stop() {
	for _, v := range ps {
		close(v.inputChan)
	}
}

func makePeers(numPeers int, minHeight, maxHeight int64) testPeers {
	peers := make(testPeers, numPeers)
	for i := 0; i < numPeers; i++ {
		peerID := p2p.ID(tmrand.Str(12))
		height := minHeight + tmrand.Int63n(maxHeight-minHeight)
		base := minHeight + int64(i)
		if base > height {
			base = height
		}
		peers[peerID] = testPeer{peerID, base, height, make(chan inputData, 10)}
	}
	return peers
}

func TestBlockPoolBasic(t *testing.T) {
	start := int64(42)
	peers := makePeers(10, start+1, 1000)
	errorsCh := make(chan peerError, 1000)
	requestsCh := make(chan BlockRequest, 1000)
	pool := NewBlockPool(start, requestsCh, errorsCh)
	pool.SetLogger(log.TestingLogger())

	err := pool.Start()
	if err != nil {
		t.Error(err)
	}

	t.Cleanup(func() {
		if err := pool.Stop(); err != nil {
			t.Error(err)
		}
	})

	peers.start()
	defer peers.stop()

	// Introduce each peer. We force base = start so every peer can serve from
	// pool.height at startup; otherwise the spec-003 updateMaxPeerHeight
	// filter (peer.base > pool.height) would exclude all of them and prevent
	// the pool from spawning any requesters.
	go func() {
		for _, peer := range peers {
			pool.SetPeerRange(peer.id, start, peer.height)
		}
	}()

	// Start a goroutine to pull blocks
	go func() {
		for {
			if !pool.IsRunning() {
				return
			}
			first, second := pool.PeekTwoBlocks()
			if first != nil && second != nil {
				pool.PopRequest()
			} else {
				time.Sleep(1 * time.Second)
			}
		}
	}()

	// Pull from channels
	for {
		select {
		case err := <-errorsCh:
			t.Error(err)
		case request := <-requestsCh:
			t.Logf("Pulled new BlockRequest %v", request)
			if request.Height == 300 {
				return // Done!
			}

			peers[request.PeerID].inputChan <- inputData{t, pool, request}
		}
	}
}

func TestBlockPoolTimeout(t *testing.T) {
	start := int64(42)
	peers := makePeers(10, start+1, 1000)
	errorsCh := make(chan peerError, 1000)
	requestsCh := make(chan BlockRequest, 1000)
	pool := NewBlockPool(start, requestsCh, errorsCh)
	pool.SetLogger(log.TestingLogger())
	err := pool.Start()
	if err != nil {
		t.Error(err)
	}
	t.Cleanup(func() {
		if err := pool.Stop(); err != nil {
			t.Error(err)
		}
	})

	for _, peer := range peers {
		t.Logf("Peer %v", peer.id)
	}

	// Introduce each peer. We force base = start so every peer can serve from
	// pool.height at startup; otherwise the spec-003 updateMaxPeerHeight
	// filter (peer.base > pool.height) would exclude all of them and prevent
	// the pool from spawning any requesters.
	go func() {
		for _, peer := range peers {
			pool.SetPeerRange(peer.id, start, peer.height)
		}
	}()

	// Start a goroutine to pull blocks
	go func() {
		for {
			if !pool.IsRunning() {
				return
			}
			first, second := pool.PeekTwoBlocks()
			if first != nil && second != nil {
				pool.PopRequest()
			} else {
				time.Sleep(1 * time.Second)
			}
		}
	}()

	// Pull from channels
	counter := 0
	timedOut := map[p2p.ID]struct{}{}
	for {
		select {
		case err := <-errorsCh:
			t.Log(err)
			// consider error to be always timeout here
			if _, ok := timedOut[err.peerID]; !ok {
				counter++
				if counter == len(peers) {
					return // Done!
				}
			}
		case request := <-requestsCh:
			t.Logf("Pulled new BlockRequest %+v", request)
		}
	}
}

func TestBlockPoolRemovePeer(t *testing.T) {
	peers := make(testPeers, 10)
	for i := 0; i < 10; i++ {
		peerID := p2p.ID(fmt.Sprintf("%d", i+1))
		height := int64(i + 1)
		peers[peerID] = testPeer{peerID, 0, height, make(chan inputData)}
	}
	requestsCh := make(chan BlockRequest)
	errorsCh := make(chan peerError)

	pool := NewBlockPool(1, requestsCh, errorsCh)
	pool.SetLogger(log.TestingLogger())
	err := pool.Start()
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := pool.Stop(); err != nil {
			t.Error(err)
		}
	})

	// add peers
	for peerID, peer := range peers {
		pool.SetPeerRange(peerID, peer.base, peer.height)
	}
	assert.EqualValues(t, 10, pool.MaxPeerHeight())

	// remove not-existing peer
	assert.NotPanics(t, func() { pool.RemovePeer(p2p.ID("Superman")) })

	// remove peer with biggest height
	pool.RemovePeer(p2p.ID("10"))
	assert.EqualValues(t, 9, pool.MaxPeerHeight())

	// remove all peers
	for peerID := range peers {
		pool.RemovePeer(peerID)
	}

	assert.EqualValues(t, 0, pool.MaxPeerHeight())
}

// ----------------------------------------------------------------------------
// spec-003-blocksync-malicious-peer-fix tests
// (CVE-2025-24371 + CometBFT issue #5801 hardening)
// ----------------------------------------------------------------------------

// TestSetPeerRange_DecreasingHeight verifies that an existing peer reporting
// a lower height than previously is removed from the pool and banned. This
// covers CVE-2025-24371 — without the check, an attacker could pin
// maxPeerHeight at an unreachable value by reporting a high height first
// and then lowering it.
func TestSetPeerRange_DecreasingHeight(t *testing.T) {
	requestsCh := make(chan BlockRequest, 10)
	errorsCh := make(chan peerError, 10)

	pool := NewBlockPool(1, requestsCh, errorsCh)
	pool.SetLogger(log.TestingLogger())

	peerID := p2p.ID("malicious")
	pool.SetPeerRange(peerID, 1, 1000)
	require.EqualValues(t, 1000, pool.MaxPeerHeight())
	require.NotNil(t, pool.peers[peerID])

	pool.SetPeerRange(peerID, 1, 1)
	require.Nil(t, pool.peers[peerID], "peer must be removed after lowering height")
	require.True(t, pool.isPeerBanned(peerID), "peer must be banned after lowering height")
	require.EqualValues(t, 0, pool.MaxPeerHeight(),
		"maxPeerHeight must drop to 0 once the only peer is removed")
}

// TestSetPeerRange_DecreasingBase verifies that an existing peer reporting
// a lower base than previously is removed and banned. Lowering base is also
// a sign of misbehavior since a peer's archived range can only grow forward.
func TestSetPeerRange_DecreasingBase(t *testing.T) {
	requestsCh := make(chan BlockRequest, 10)
	errorsCh := make(chan peerError, 10)

	pool := NewBlockPool(100, requestsCh, errorsCh)
	pool.SetLogger(log.TestingLogger())

	peerID := p2p.ID("malicious")
	pool.SetPeerRange(peerID, 50, 1000)
	require.NotNil(t, pool.peers[peerID])

	pool.SetPeerRange(peerID, 10, 1000)
	require.Nil(t, pool.peers[peerID], "peer must be removed after lowering base")
	require.True(t, pool.isPeerBanned(peerID), "peer must be banned after lowering base")
}

// TestSetPeerRange_BaseGreaterThanHeight verifies that a peer reporting a
// base greater than its own height (a structurally impossible state) is
// banned immediately and never enters the pool, ensuring it cannot poison
// maxPeerHeight (CometBFT issue #5801).
func TestSetPeerRange_BaseGreaterThanHeight(t *testing.T) {
	requestsCh := make(chan BlockRequest, 10)
	errorsCh := make(chan peerError, 10)

	pool := NewBlockPool(1, requestsCh, errorsCh)
	pool.SetLogger(log.TestingLogger())

	peerID := p2p.ID("malicious")
	pool.SetPeerRange(peerID, 500, 100)
	require.Nil(t, pool.peers[peerID], "peer with base > height must not be added")
	require.True(t, pool.isPeerBanned(peerID))
	require.EqualValues(t, 0, pool.MaxPeerHeight())
}

// TestBanPeer_PreventsReentry verifies that a banned peer cannot reintroduce
// itself with a fresh — even otherwise valid — StatusResponse during the
// ban window.
func TestBanPeer_PreventsReentry(t *testing.T) {
	requestsCh := make(chan BlockRequest, 10)
	errorsCh := make(chan peerError, 10)

	pool := NewBlockPool(1, requestsCh, errorsCh)
	pool.SetLogger(log.TestingLogger())

	peerID := p2p.ID("malicious")
	pool.SetPeerRange(peerID, 500, 100)
	require.True(t, pool.isPeerBanned(peerID))

	pool.SetPeerRange(peerID, 1, 200)
	require.Nil(t, pool.peers[peerID], "banned peer must not be re-added during ban window")
	require.EqualValues(t, 0, pool.MaxPeerHeight())
}

// TestBanPeer_ExpiryAllowsReentry verifies that once the ban window expires
// the peer can rejoin the pool normally and contributes to maxPeerHeight
// again.
func TestBanPeer_ExpiryAllowsReentry(t *testing.T) {
	origDuration := peerBanDuration
	peerBanDuration = 10 * time.Millisecond
	t.Cleanup(func() { peerBanDuration = origDuration })

	requestsCh := make(chan BlockRequest, 10)
	errorsCh := make(chan peerError, 10)

	pool := NewBlockPool(1, requestsCh, errorsCh)
	pool.SetLogger(log.TestingLogger())

	peerID := p2p.ID("rehabilitated")
	pool.SetPeerRange(peerID, 500, 100)
	require.True(t, pool.isPeerBanned(peerID))

	time.Sleep(20 * time.Millisecond)

	pool.SetPeerRange(peerID, 1, 200)
	require.NotNil(t, pool.peers[peerID], "peer should be re-admitted after ban expiry")
	require.False(t, pool.isPeerBanned(peerID))
	require.EqualValues(t, 200, pool.MaxPeerHeight())
}

// TestBlockPoolMaxPeerHeightNotPoisonedByHighBase covers the core fix for
// CometBFT issue #5801. A malicious peer broadcasting an inflated base/height
// pair (e.g. base=1_000_000) must not raise maxPeerHeight beyond what honest
// peers can actually serve, because that would stall IsCaughtUp() forever.
//
// We start the pool at the honest peer's tip so that, with the malicious
// peer correctly filtered, IsCaughtUp can succeed. Without the fix,
// maxPeerHeight would be ~1_000_100 and IsCaughtUp would return false
// indefinitely.
func TestBlockPoolMaxPeerHeightNotPoisonedByHighBase(t *testing.T) {
	requestsCh := make(chan BlockRequest, 10)
	errorsCh := make(chan peerError, 10)

	pool := NewBlockPool(200, requestsCh, errorsCh)
	pool.SetLogger(log.TestingLogger())

	pool.SetPeerRange(p2p.ID("honest"), 1, 200)
	require.EqualValues(t, 200, pool.MaxPeerHeight())

	pool.SetPeerRange(p2p.ID("malicious"), 1_000_000, 1_000_100)
	require.EqualValues(t, 200, pool.MaxPeerHeight(),
		"malicious peer with base above pool.height must not raise maxPeerHeight")

	require.True(t, pool.IsCaughtUp(),
		"with malicious peer filtered, the node must be able to declare itself caught up")
}

// TestBlockPoolMaxPeerHeightRefreshesOnPopRequest verifies that a peer whose
// base is initially above pool.height — and therefore filtered out of
// maxPeerHeight — is re-introduced once pool.height advances past its base
// via PopRequest. This guards the segmented-peer scenario from the spec.
func TestBlockPoolMaxPeerHeightRefreshesOnPopRequest(t *testing.T) {
	requestsCh := make(chan BlockRequest, 10)
	errorsCh := make(chan peerError, 10)

	pool := NewBlockPool(10, requestsCh, errorsCh)
	pool.SetLogger(log.TestingLogger())

	pool.SetPeerRange(p2p.ID("A"), 1, 20)
	pool.SetPeerRange(p2p.ID("B"), 15, 100)
	require.EqualValues(t, 20, pool.MaxPeerHeight(),
		"peer B (base=15) must not contribute while pool.height (10) is below its base")

	// Advance pool.height from 10 to 15 by installing dummy requesters and
	// popping them. The dummy requester is never started, so Stop() is a
	// logged no-op.
	for h := int64(10); h < 15; h++ {
		pool.mtx.Lock()
		pool.requesters[h] = newBPRequester(pool, h)
		pool.mtx.Unlock()
		pool.PopRequest()
	}

	require.EqualValues(t, 100, pool.MaxPeerHeight(),
		"peer B must contribute to maxPeerHeight once pool.height reaches its base")
}

// TestSegmentedPeers_BoundaryDeadlockPrevention validates the segmented
// peer scenario from spec-003 §3.3: peer A pinned at pool.height, peer B
// starting one block above. Without the spec-003 fix, peer B's height
// would inflate maxPeerHeight and IsCaughtUp would block forever waiting
// for blocks no peer can serve. With the fix, we accept a deliberate
// liveness trade-off: peer B is filtered until pool.height advances past
// its base, so IsCaughtUp returns true at the boundary and the node exits
// blocksync rather than stalling.
func TestSegmentedPeers_BoundaryDeadlockPrevention(t *testing.T) {
	requestsCh := make(chan BlockRequest, 10)
	errorsCh := make(chan peerError, 10)

	pool := NewBlockPool(150, requestsCh, errorsCh)
	pool.SetLogger(log.TestingLogger())
	// Pretend the pool has been running for a while so IsCaughtUp's
	// receivedBlockOrTimedOut precondition is satisfied independently of
	// the maxPeerHeight branch we want to exercise.
	pool.startTime = time.Now().Add(-10 * time.Second)

	pool.SetPeerRange(p2p.ID("A"), 1, 150)
	pool.SetPeerRange(p2p.ID("B"), 151, 500)

	require.EqualValues(t, 150, pool.MaxPeerHeight(),
		"peer B must be filtered while its base (151) > pool.height (150)")
	require.True(t, pool.IsCaughtUp(),
		"with peer B filtered, IsCaughtUp must succeed and avoid the deadlock")

	// Once pool.height advances past peer B's base, peer B becomes eligible
	// and lifts maxPeerHeight; the node correctly recognises it is no longer
	// caught up and resumes syncing.
	pool.mtx.Lock()
	pool.requesters[150] = newBPRequester(pool, 150)
	pool.mtx.Unlock()
	pool.PopRequest()

	require.EqualValues(t, 500, pool.MaxPeerHeight(),
		"peer B must contribute to maxPeerHeight after pool.height advances to 151")
	require.False(t, pool.IsCaughtUp(),
		"after peer B is re-admitted, IsCaughtUp must report we still have blocks to sync")
}
