package sequencer

import (
	"fmt"
	"math/rand"
	"reflect"
	"sync"
	"time"

	"github.com/cosmos/gogoproto/proto"
	"github.com/morph-l2/go-ethereum/common"

	"github.com/tendermint/tendermint/libs/log"
	"github.com/tendermint/tendermint/p2p"
	bcproto "github.com/tendermint/tendermint/proto/tendermint/blocksync"
	"github.com/tendermint/tendermint/types"
)

const (
	BlockBroadcastChannel = byte(0x50) // For block broadcast (requires signature verification)
	SequencerSyncChannel  = byte(0x51) // For block sync requests (no signature verification)

	// TODO: make these parameters configurable
	smallGapThreshold    = 20   // Gap for direct block request
	recentBlocksCapacity = 1000 // Recent applied blocks cache
	seenBlocksCapacity   = 2000 // Seen blocks for dedup
	peerSentCapacity     = 500  // Per-peer sent tracking
	applyInterval        = 10 * time.Second
	syncInterval         = 10 * time.Second

	// Sync request tracking
	syncRequestTTL         = 20 * time.Second
	maxPendingSyncRequests = 256
	maxPendingSyncPerPeer  = 64
)

// SyncReq represents a pending sync channel request.
type SyncReq struct {
	Height   int64
	PeerID   p2p.ID
	ExpireAt time.Time
}

// BlockPool interface (avoids import cycle)
type BlockPool interface {
	MaxPeerHeight() int64
	GetPeerHeight(peerID p2p.ID) int64
	IsRunning() bool
}

// BlockBroadcastReactor handles block broadcast in sequencer mode.
type BlockBroadcastReactor struct {
	p2p.BaseReactor

	stateV2  *StateV2
	pool     BlockPool
	waitSync bool

	recentBlocks *BlockRingBuffer   // Applied blocks for peer requests
	pendingCache *PendingBlockCache // Pending blocks

	// Gossip state (bounded capacity, auto-evict old entries)
	seenBlocks *HashSet     // Blocks we've seen (dedup)
	peerSent   *PeerHashSet // Blocks sent to each peer

	// applyMtx serializes the "check parent + apply + save signature" sequence
	// in the reactor's applyBlock path. StateV2.ApplyBlock has its own mutex
	// for the actual apply, but applyMtx ensures the parent-hash check and
	// signature persistence are also atomic with the apply.
	applyMtx         sync.Mutex
	startMu          sync.Mutex
	sequencerStarted bool
	logger           log.Logger

	verifier SequencerVerifier
	sigStore *SignatureStore

	// syncRequests tracks pending sync channel requests, keyed by height.
	// Used to reject unsolicited responses before decode/verification.
	// syncPeerCounts is a secondary index for O(1) per-peer count lookup.
	syncRequestsMu sync.Mutex
	syncRequests   map[int64]*SyncReq
	syncPeerCounts map[p2p.ID]int
}

// NewBlockBroadcastReactor creates a new reactor.
func NewBlockBroadcastReactor(
	pool BlockPool,
	stateV2 *StateV2,
	waitSync bool,
	logger log.Logger,
	verifier SequencerVerifier,
	sigStore *SignatureStore,
) *BlockBroadcastReactor {
	r := &BlockBroadcastReactor{
		pool:           pool,
		stateV2:        stateV2,
		waitSync:       waitSync,
		recentBlocks:   NewBlockRingBuffer(recentBlocksCapacity),
		pendingCache:   NewPendingBlockCache(),
		seenBlocks:     NewHashSet(seenBlocksCapacity),
		peerSent:       NewPeerHashSet(peerSentCapacity),
		syncRequests:   make(map[int64]*SyncReq),
		syncPeerCounts: make(map[p2p.ID]int),
		logger:         logger.With("module", "broadcastReactor"),
		verifier:       verifier,
		sigStore:       sigStore,
	}
	r.BaseReactor = *p2p.NewBaseReactor("BlockBroadcast", r)
	return r
}

func (r *BlockBroadcastReactor) SetLogger(l log.Logger) {
	r.BaseService.Logger = l
	r.logger = l.With("module", "broadcastReactor")
}

func (r *BlockBroadcastReactor) OnStart() error {
	if r.waitSync {
		// Don't start sequencer routines on start if we are in wait sync mode
		// will be started via StartSequencerRoutines
		return nil
	}
	return r.StartSequencerRoutines()
}

func (r *BlockBroadcastReactor) StartSequencerRoutines() error {
	r.startMu.Lock()
	defer r.startMu.Unlock()

	if r.sequencerStarted {
		return nil
	}

	r.waitSync = false

	if r.stateV2 != nil && !r.stateV2.IsRunning() {
		if err := r.stateV2.Start(); err != nil {
			return fmt.Errorf("failed to start StateV2: %w", err)
		}
	}

	// Fullnode (no signer): only applyRoutine (P2P sync)
	// Nodes with signer (ActiveSeq / HA-Leader / HA-Follower): only broadcastRoutine
	//   - roleCheckRoutine is started by StateV2.OnStart()
	//   - applyRoutine is NOT started (sequencer produces, HA uses Raft)
	if r.stateV2.HasSigner() {
		go r.broadcastRoutine()
	} else {
		go r.applyRoutine()
	}

	r.sequencerStarted = true
	return nil
}

func (r *BlockBroadcastReactor) OnStop() {
	r.logger.Info("Stopping BlockBroadcastReactor")
}

func (r *BlockBroadcastReactor) GetChannels() []*p2p.ChannelDescriptor {
	return []*p2p.ChannelDescriptor{
		{ID: BlockBroadcastChannel, Priority: 6, SendQueueCapacity: 1000, RecvBufferCapacity: 50 * 4096},
		{ID: SequencerSyncChannel, Priority: 5, SendQueueCapacity: 1000, RecvBufferCapacity: 50 * 4096},
	}
}

func (r *BlockBroadcastReactor) AddPeer(peer p2p.Peer) {
	r.peerSent.AddPeer(string(peer.ID()))
}

func (r *BlockBroadcastReactor) RemovePeer(peer p2p.Peer, reason interface{}) {
	r.peerSent.RemovePeer(string(peer.ID()))
	r.removeSyncRequestsByPeer(peer.ID())
}

func (r *BlockBroadcastReactor) Receive(chID byte, src p2p.Peer, msgBytes []byte) {
	r.logger.Debug("Receive message", "chId", chID, "src", src.ID(), "len", len(msgBytes))
	msg, err := decodeMsg(msgBytes)
	if err != nil {
		r.logger.Error("Error decoding message", "src", src, "chId", chID, "err", err)
		r.Switch.StopPeerForError(src, err)
		return
	}

	switch chID {
	case BlockBroadcastChannel:
		// Broadcast channel: requires signature verification
		r.handleBroadcastMsg(msg, src)
	case SequencerSyncChannel:
		// Sync channel: no signature verification
		r.handleSyncMsg(msg, src)
	default:
		r.logger.Error("Unknown channel", "chId", chID)
	}
}

func (r *BlockBroadcastReactor) handleBroadcastMsg(msg interface{}, src p2p.Peer) {
	switch msg := msg.(type) {
	case *bcproto.BlockResponseV2:
		if msg.Block != nil {
			blockV2, err := types.BlockV2FromProto(msg.Block)
			if err != nil {
				r.logger.Error("Invalid BlockV2", "err", err)
				return
			}
			r.onBlockV2(blockV2, src, true) // from broadcast channel
			r.logger.Debug("handleBroadcastMsg", "src", src, "height", blockV2.Number, "hash", blockV2.Hash.Hex())
		}
	default:
		r.logger.Debug("Ignoring non-block message on broadcast channel", "type", reflect.TypeOf(msg))
	}
}

func (r *BlockBroadcastReactor) handleSyncMsg(msg interface{}, src p2p.Peer) {
	switch msg := msg.(type) {
	case *bcproto.BlockRequest:
		r.onBlockRequest(msg, src)
		r.logger.Debug("handleSyncMsg block request", "msg", msg, "src", src, "height", msg.Height)
	case *bcproto.BlockResponseV2:
		if msg.Block != nil {
			// Check request before full decode to reject unsolicited responses early
			height := int64(msg.Block.Number)
			if !r.checkAndTakeSyncRequest(src.ID(), height) {
				r.logger.Error("Unsolicited sync response, dropping",
					"peer", src.ID(), "height", height)
				return
			}
			blockV2, err := types.BlockV2FromProto(msg.Block)
			if err != nil {
				r.logger.Error("Invalid BlockV2", "err", err)
				return
			}
			r.onBlockV2(blockV2, src, false) // from sync channel
			r.logger.Debug("handleSyncMsg block response", "src", src, "height", blockV2.Number, "hash", blockV2.Hash.Hex())
		}
	case *bcproto.NoBlockResponse:
		if !r.checkAndTakeSyncRequest(src.ID(), msg.Height) {
			r.logger.Error("Unsolicited NoBlockResponse, dropping", "peer", src.ID(), "height", msg.Height)
			return
		}
		r.logger.Debug("Peer does not have requested block", "peer", src, "height", msg.Height)
	default:
		r.logger.Debug("Ignoring unknown message on sync channel", "type", reflect.TypeOf(msg))
	}
}

// ============================================================================
// Routines
// ============================================================================

// broadcastRoutine: listen to block source and broadcast to P2P.
// Data source: HA mode reads from ha.Subscribe(), non-HA from broadcastCh.
// HA Follower consumes channel to prevent buildup but does not broadcast.
func (r *BlockBroadcastReactor) broadcastRoutine() {
	r.logger.Info("Starting block broadcast routine")

	var source <-chan *BlockV2
	if r.stateV2.IsHAMode() {
		source = r.stateV2.HASubscribe()
	} else {
		source = r.stateV2.BroadcastCh()
	}

	for {
		select {
		case <-r.Quit():
			return
		case block := <-source:
			// Always populate recentBlocks so the cache is warm if this follower
			// is promoted to leader and starts serving block requests from fullnodes.
			r.recentBlocks.Add(block)
			// HA Follower: do not broadcast, only drain the channel.
			if r.stateV2.IsHAMode() && !r.stateV2.IsHALeader() {
				continue
			}
			r.broadcast(block)
		}
	}
}

// applyRoutine: periodically try to apply blocks from unlink cache
func (r *BlockBroadcastReactor) applyRoutine() {
	r.logger.Info("Starting block apply routine")
	tickerApply := time.NewTicker(applyInterval)
	tickerSync := time.NewTicker(syncInterval)
	defer tickerSync.Stop()
	defer tickerApply.Stop()

	for {
		select {
		case <-r.Quit():
			return
		case <-tickerApply.C:
			r.tryApplyFromCache()
		case <-tickerSync.C:
			r.checkSyncGap()
		}
	}
}

// ============================================================================
// Core Logic
// ============================================================================

// onBlockV2: receive block from peer.
// fromBroadcast: true for broadcast channel (triggers gossip), false for sync channel.
func (r *BlockBroadcastReactor) onBlockV2(block *BlockV2, src p2p.Peer, fromBroadcast bool) {
	// Only Fullnode (no signer) processes P2P blocks:
	//   - ActiveSequencer: sole producer, P2P blocks are only echoes or invalid
	//   - HA nodes: receive blocks via Raft, not P2P
	if r.stateV2.HasSigner() {
		return
	}

	r.logger.Debug("onBlockV2", "number", block.Number, "hash", block.Hash.Hex(), "fromBroadcast", fromBroadcast)

	// Dedup: only for broadcast channel
	if fromBroadcast && r.markSeen(block.Hash) {
		r.logger.Debug("onBlockV2 broadcast dedup", "number", block.Number, "hash", block.Hash.Hex())
		return
	}

	// Unified signature verification for all incoming blocks
	if err := VerifyBlockSignature(r.verifier, block); err != nil {
		r.logger.Error("Block signature verification failed, discarding",
			"number", block.Number, "hash", block.Hash.Hex(), "err", err)
		return
	}

	// Mark as received from this peer (don't send back)
	r.markSentToPeer(src.ID(), block.Hash)

	localHeight := r.stateV2.LatestHeight()

	if r.isNextBlock(block) {
		if err := r.applyBlock(block); err != nil {
			r.logger.Error("Apply failed", "number", block.Number, "hash", block.Hash.Hex(), "err", err)
			r.pendingCache.Add(block, uint64(localHeight))
			return
		}
		if fromBroadcast {
			r.gossipBlock(block, src.ID())
		}
	} else {
		r.pendingCache.Add(block, uint64(localHeight))
	}
}

// tryApplyFromCache: apply blocks from unlink cache (called by applyRoutine)
// Blocks in cache don't need signature verification (already verified or from sync)
func (r *BlockBroadcastReactor) tryApplyFromCache() {
	currentBlock := r.stateV2.LatestBlock()
	if currentBlock == nil {
		return
	}
	r.logger.Debug("tryApplyFromCache currentBlock",
		"number", currentBlock.Number,
		"hash", currentBlock.Hash.Hex(),
		"pendingCache size", r.pendingCache.Size())

	// Get longest chain from current head
	chain := r.pendingCache.GetLongestChain(currentBlock.Hash)
	for i, block := range chain {
		r.logger.Debug("pendingCache chain", "index", i, "number", block.Number, "hash", block.Hash.Hex(), "parentHash", block.ParentHash.Hex())
		if !r.isNextBlock(block) {
			break
		}
		r.logger.Debug("Trying to apply from cache", "number", block.Number, "hash", block.Hash.Hex())
		if err := r.applyBlock(block); err != nil {
			r.logger.Error("Apply from cache failed", "number", block.Number, "err", err)
			break
		}
	}

	// Prune old blocks (keep some for potential reorg)
	localHeight := uint64(r.stateV2.LatestHeight())
	if localHeight > MaxPendingHeightBehind {
		r.pendingCache.PruneBelow(localHeight - MaxPendingHeightBehind)
	}
}

// checkSyncGap: request missing blocks via SequencerSyncChannel
// All sync requests go through this method (no longer uses blocksync pool)
func (r *BlockBroadcastReactor) checkSyncGap() {
	localHeight := r.stateV2.LatestHeight()
	maxPeerHeight := r.pool.MaxPeerHeight()
	gap := maxPeerHeight - localHeight
	r.logger.Debug("Checking sync goroutines", "gap", gap, "localHeight", localHeight, "maxPeerHeight", maxPeerHeight)
	if gap <= smallGapThreshold {
		return
	}

	// Request missing blocks (limited to smallGapThreshold per cycle to avoid spam)
	r.requestMissingBlocks(localHeight+1, maxPeerHeight)
}

// requestMissingBlocks requests blocks in range [start, end] from peers.
// Respects maxOutstandingTotal, maxOutstandingPerPeer, and maxNewRequestsPerTick limits.
func (r *BlockBroadcastReactor) requestMissingBlocks(start, end int64) {
	r.cleanupExpiredRequests()

	peers := r.Switch.Peers().List()
	if len(peers) == 0 {
		return
	}

	for height := start; height <= end; height++ {
		if r.pendingSyncCount() >= maxPendingSyncRequests {
			break
		}
		// Skip if already have a pending request for this height
		r.syncRequestsMu.Lock()
		_, exists := r.syncRequests[height]
		r.syncRequestsMu.Unlock()
		if exists {
			continue
		}

		// findPeerWithHeight already skips peers exceeding per-peer limit
		peer := r.findPeerWithHeight(peers, height)
		if peer == nil {
			continue
		}

		msg := &bcproto.BlockRequest{Height: height}
		bz, err := encodeMsg(msg)
		if err != nil {
			r.logger.Error("Failed to encode BlockRequest", "height", height, "err", err)
			continue
		}
		if !peer.TrySend(SequencerSyncChannel, bz) {
			r.logger.Error("Failed to send BlockRequest", "height", height, "peer", peer.ID())
			continue
		}
		r.recordSyncRequest(peer.ID(), height)
	}
}

// findPeerWithHeight finds a random peer that has the given height and is within per-peer request limit.
// Uses random start index to avoid always hitting the same peer.
func (r *BlockBroadcastReactor) findPeerWithHeight(peers []p2p.Peer, height int64) p2p.Peer {
	n := len(peers)
	if n == 0 {
		return nil
	}

	start := rand.Intn(n) //nolint:gosec // non-security: peer selection randomization
	for i := 0; i < n; i++ {
		peer := peers[(start+i)%n]
		if r.pool.GetPeerHeight(peer.ID()) >= height &&
			r.pendingSyncCountByPeer(peer.ID()) < maxPendingSyncPerPeer {
			return peer
		}
	}
	return nil
}

// isNextBlock checks if block can be applied (height and parent match).
// Used for quick pre-check before acquiring lock.
func (r *BlockBroadcastReactor) isNextBlock(block *BlockV2) bool {
	currentBlock := r.stateV2.LatestBlock()
	if currentBlock == nil {
		// First block after upgrade
		return block.Number == uint64(r.stateV2.LatestHeight())+1
	}
	return block.Number == currentBlock.Number+1 && block.ParentHash == currentBlock.Hash
}

// applyBlock verifies signature, applies the block atomically, and persists the signature.
// Thread-safe: uses mutex to ensure sequential block application.
func (r *BlockBroadcastReactor) applyBlock(block *BlockV2) error {
	r.applyMtx.Lock()
	defer r.applyMtx.Unlock()

	// Verify parent
	currentBlock := r.stateV2.LatestBlock()
	if currentBlock != nil && block.ParentHash != currentBlock.Hash {
		return fmt.Errorf("parent mismatch")
	}

	// ApplyBlock handles geth apply + SaveSignature in one call.
	if err := r.stateV2.ApplyBlock(block); err != nil {
		return err
	}

	// Add to recent blocks
	r.recentBlocks.Add(block)

	r.logger.Info("Applied block", "number", block.Number)
	return nil
}

// ============================================================================
// Gossip
// ============================================================================

// markSeen marks a block as seen. Returns true if already seen (duplicate).
func (r *BlockBroadcastReactor) markSeen(hash common.Hash) bool {
	return r.seenBlocks.Add(hash) // Returns true if already existed
}

// markSentToPeer marks a block as sent to a peer.
func (r *BlockBroadcastReactor) markSentToPeer(peerID p2p.ID, hash common.Hash) {
	r.peerSent.Add(string(peerID), hash)
}

// hasSentToPeer checks if block was sent to peer.
func (r *BlockBroadcastReactor) hasSentToPeer(peerID p2p.ID, hash common.Hash) bool {
	return r.peerSent.Contains(string(peerID), hash)
}

// gossipBlock forwards a block to all peers except the source.
// TODO: randomize picking the peers to gossip to avoid flooding the network
func (r *BlockBroadcastReactor) gossipBlock(block *BlockV2, fromPeer p2p.ID) {
	r.logger.Info("Gossiping block", "number", block.Number, "hash", block.Hash.Hex(), "fromPeer", fromPeer)
	msg := &bcproto.BlockResponseV2{
		Block: types.BlockV2ToProto(block),
	}
	bz, err := encodeMsg(msg)
	if err != nil {
		r.logger.Error("Failed to encode BlockResponseV2 for gossip", "number", block.Number, "err", err)
		return
	}

	peers := r.Switch.Peers().List()
	for _, peer := range peers {
		peerID := peer.ID()

		// Skip source peer
		if peerID == fromPeer {
			continue
		}

		// Skip if already sent
		if r.hasSentToPeer(peerID, block.Hash) {
			continue
		}

		// Send and mark
		if peer.TrySend(BlockBroadcastChannel, bz) {
			r.markSentToPeer(peerID, block.Hash)
		}
	}
}

// ============================================================================
// Sync Request Tracking
// ============================================================================

// recordSyncRequest records a pending sync request keyed by height.
func (r *BlockBroadcastReactor) recordSyncRequest(peerID p2p.ID, height int64) {
	r.syncRequestsMu.Lock()
	defer r.syncRequestsMu.Unlock()
	if old, ok := r.syncRequests[height]; ok {
		r.syncPeerCounts[old.PeerID]--
	}
	r.syncRequests[height] = &SyncReq{
		Height:   height,
		PeerID:   peerID,
		ExpireAt: time.Now().Add(syncRequestTTL),
	}
	r.syncPeerCounts[peerID]++
}

// checkAndTakeSyncRequest validates a sync response against pending requests.
// Only removes the entry if peerID matches and request is not expired.
func (r *BlockBroadcastReactor) checkAndTakeSyncRequest(peerID p2p.ID, height int64) bool {
	r.syncRequestsMu.Lock()
	defer r.syncRequestsMu.Unlock()

	req, ok := r.syncRequests[height]
	if !ok || req.PeerID != peerID || time.Now().After(req.ExpireAt) {
		return false
	}
	delete(r.syncRequests, height)
	r.syncPeerCounts[peerID]--
	return true
}

// cleanupExpiredRequests removes expired entries. Called before sending new requests.
func (r *BlockBroadcastReactor) cleanupExpiredRequests() {
	r.syncRequestsMu.Lock()
	defer r.syncRequestsMu.Unlock()
	now := time.Now()
	for h, req := range r.syncRequests {
		if now.After(req.ExpireAt) {
			r.syncPeerCounts[req.PeerID]--
			delete(r.syncRequests, h)
		}
	}
}

// pendingSyncCount returns the total number of pending sync requests.
func (r *BlockBroadcastReactor) pendingSyncCount() int {
	r.syncRequestsMu.Lock()
	defer r.syncRequestsMu.Unlock()
	return len(r.syncRequests)
}

// pendingSyncCountByPeer returns the number of pending sync requests for a specific peer.
func (r *BlockBroadcastReactor) pendingSyncCountByPeer(peerID p2p.ID) int {
	r.syncRequestsMu.Lock()
	defer r.syncRequestsMu.Unlock()
	return r.syncPeerCounts[peerID]
}

// removeSyncRequestsByPeer removes all pending requests for a disconnected peer.
func (r *BlockBroadcastReactor) removeSyncRequestsByPeer(peerID p2p.ID) {
	r.syncRequestsMu.Lock()
	defer r.syncRequestsMu.Unlock()
	for h, req := range r.syncRequests {
		if req.PeerID == peerID {
			delete(r.syncRequests, h)
		}
	}
	delete(r.syncPeerCounts, peerID)
}

// ============================================================================
// Message Handlers
// ============================================================================

func (r *BlockBroadcastReactor) onBlockRequest(msg *bcproto.BlockRequest, src p2p.Peer) {
	// Try to get from recent blocks cache first
	block := r.recentBlocks.GetByHeight(uint64(msg.Height))

	// If not in cache, try to get from l2node
	if block == nil {
		var err error
		block, err = r.stateV2.GetBlockByNumber(uint64(msg.Height))
		if err != nil {
			r.logger.Debug("Failed to get block from l2node", "height", msg.Height, "err", err)
		}
	}

	// If still not found, send NoBlockResponse
	if block == nil {
		bz, err := encodeMsg(&bcproto.NoBlockResponse{Height: msg.Height})
		if err != nil {
			r.logger.Error("Failed to encode NoBlockResponse", "height", msg.Height, "err", err)
			return
		}
		src.TrySend(SequencerSyncChannel, bz)
		return
	}

	// Attach signature from store if block doesn't already carry one
	if len(block.Signature) == 0 && r.sigStore != nil {
		if sig, err := r.sigStore.GetSignature(block.Hash); err == nil {
			block.Signature = sig
		}
	}

	resp := &bcproto.BlockResponseV2{
		Block: types.BlockV2ToProto(block),
	}
	bz, err := encodeMsg(resp)
	if err != nil {
		r.logger.Error("Failed to encode BlockResponseV2", "height", msg.Height, "err", err)
		return
	}
	src.TrySend(SequencerSyncChannel, bz) // Respond on sync channel
}

// broadcast only for sequencer
func (r *BlockBroadcastReactor) broadcast(block *BlockV2) {
	resp := &bcproto.BlockResponseV2{
		Block: types.BlockV2ToProto(block),
	}
	bz, err := encodeMsg(resp)
	if err != nil {
		r.logger.Error("Failed to encode BlockResponseV2 for broadcast", "number", block.Number, "err", err)
		return
	}
	r.Switch.Broadcast(BlockBroadcastChannel, bz)
	r.logger.Info("Broadcast block", "number", block.Number, "hash", block.Hash.Hex())
}


// ==================== Message Encoding/Decoding ====================
// Local copies to avoid import cycle with blocksync package

func encodeMsg(pb proto.Message) ([]byte, error) {
	msg := bcproto.Message{}
	switch pb := pb.(type) {
	case *bcproto.BlockRequest:
		msg.Sum = &bcproto.Message_BlockRequest{BlockRequest: pb}
	case *bcproto.BlockResponse:
		msg.Sum = &bcproto.Message_BlockResponse{BlockResponse: pb}
	case *bcproto.BlockResponseV2:
		msg.Sum = &bcproto.Message_BlockResponseV2{BlockResponseV2: pb}
	case *bcproto.NoBlockResponse:
		msg.Sum = &bcproto.Message_NoBlockResponse{NoBlockResponse: pb}
	case *bcproto.StatusRequest:
		msg.Sum = &bcproto.Message_StatusRequest{StatusRequest: pb}
	case *bcproto.StatusResponse:
		msg.Sum = &bcproto.Message_StatusResponse{StatusResponse: pb}
	default:
		return nil, fmt.Errorf("unknown message type %T", pb)
	}
	return proto.Marshal(&msg)
}

func decodeMsg(bz []byte) (proto.Message, error) {
	pb := &bcproto.Message{}
	if err := proto.Unmarshal(bz, pb); err != nil {
		return nil, err
	}
	switch msg := pb.Sum.(type) {
	case *bcproto.Message_BlockRequest:
		return msg.BlockRequest, nil
	case *bcproto.Message_BlockResponse:
		return msg.BlockResponse, nil
	case *bcproto.Message_BlockResponseV2:
		return msg.BlockResponseV2, nil
	case *bcproto.Message_NoBlockResponse:
		return msg.NoBlockResponse, nil
	case *bcproto.Message_StatusRequest:
		return msg.StatusRequest, nil
	case *bcproto.Message_StatusResponse:
		return msg.StatusResponse, nil
	default:
		return nil, fmt.Errorf("unknown message type %T", msg)
	}
}
