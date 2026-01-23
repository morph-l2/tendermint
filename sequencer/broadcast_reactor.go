package sequencer

import (
	"fmt"
	"math/big"
	"math/rand"
	"reflect"
	"sync"
	"time"

	"github.com/tendermint/tendermint/upgrade"

	"github.com/cosmos/gogoproto/proto"
	"github.com/morph-l2/go-ethereum/common"
	"github.com/morph-l2/go-ethereum/crypto"

	"github.com/tendermint/tendermint/libs/log"
	"github.com/tendermint/tendermint/p2p"
	bcproto "github.com/tendermint/tendermint/proto/tendermint/blocksync"
	seqproto "github.com/tendermint/tendermint/proto/tendermint/sequencer"
	"github.com/tendermint/tendermint/types"
)

const (
	BlockBroadcastChannel = byte(0x50) // For block broadcast (requires signature verification)
	SequencerSyncChannel  = byte(0x51) // For block sync requests (no signature verification)

	// TODO: make these parameters configurable
	smallGapThreshold    = 5    // Gap for direct block request
	recentBlocksCapacity = 1000 // Recent applied blocks cache
	seenBlocksCapacity   = 2000 // Seen blocks for dedup
	peerSentCapacity     = 500  // Per-peer sent tracking
	applyInterval        = 500 * time.Millisecond
)

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

	applyMtx         sync.Mutex // Protects applyBlock to ensure sequential block application
	sequencerStarted bool       // True when sequencer mode is actually running (not just registered)
	logger           log.Logger
}

// NewBlockBroadcastReactor creates a new reactor.
func NewBlockBroadcastReactor(pool BlockPool, stateV2 *StateV2, waitSync bool, logger log.Logger) *BlockBroadcastReactor {
	r := &BlockBroadcastReactor{
		pool:         pool,
		stateV2:      stateV2,
		waitSync:     waitSync,
		recentBlocks: NewBlockRingBuffer(recentBlocksCapacity),
		pendingCache: NewPendingBlockCache(),
		seenBlocks:   NewHashSet(seenBlocksCapacity),
		peerSent:     NewPeerHashSet(peerSentCapacity),
		logger:       logger.With("module", "broadcastReactor"),
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
	if r.sequencerStarted {
		r.logger.Error("Sequencer routines already started, skipping")
		return nil
	}

	r.waitSync = false

	if r.stateV2 != nil && !r.stateV2.IsRunning() {
		if err := r.stateV2.Start(); err != nil {
			return fmt.Errorf("failed to start StateV2: %w", err)
		}
	}

	if upgrade.IsSequencer(r.stateV2.seqAddr) {
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

// func (r *BlockBroadcastReactor) SwitchToSequencer() error {
// 	r.logger.Info("Sync mode switching to sequencer mode")
// 	r.waitSync = false
// 	return r.StartSequencerRoutines()
// }

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
			r.onBlockV2(blockV2, src, true) // verify signature
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
			blockV2, err := types.BlockV2FromProto(msg.Block)
			if err != nil {
				r.logger.Error("Invalid BlockV2", "err", err)
				return
			}
			r.onBlockV2(blockV2, src, false) // no signature verification
			r.logger.Debug("handleSyncMsg block response", "src", src, "height", blockV2.Number, "hash", blockV2.Hash.Hex())
		}
	case *bcproto.NoBlockResponse:
		r.logger.Debug("Peer does not have requested block", "peer", src, "height", msg.Height)
	default:
		r.logger.Debug("Ignoring unknown message on sync channel", "type", reflect.TypeOf(msg))
	}
}

// ============================================================================
// Routines
// ============================================================================

// broadcastRoutine: listen to stateV2 and broadcast new blocks
func (r *BlockBroadcastReactor) broadcastRoutine() {
	r.logger.Info("Starting block broadcast routine")
	for {
		select {
		case <-r.Quit():
			return
		case block := <-r.stateV2.BroadcastCh():
			r.recentBlocks.Add(block)
			r.broadcast(block)
		}
	}
}

// applyRoutine: periodically try to apply blocks from unlink cache
func (r *BlockBroadcastReactor) applyRoutine() {
	r.logger.Info("Starting block apply routine")
	ticker := time.NewTicker(applyInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.Quit():
			return
		case <-ticker.C:
			r.tryApplyFromCache()
			r.checkSyncGap()
		}
	}
}

// ============================================================================
// Core Logic
// ============================================================================

// onBlockV2: receive block from peer
// verifySig: true for broadcast channel, false for sync channel
func (r *BlockBroadcastReactor) onBlockV2(block *BlockV2, src p2p.Peer, verifySig bool) {
	r.logger.Debug("onBlockV2", "number", block.Number, "hash", block.Hash.Hex(), "verifySig", verifySig)
	// Dedup: skip if already seen
	if r.markSeen(block.Hash) {
		r.logger.Debug("onBlockV2 dedup", "number", block.Number, "hash", block.Hash.Hex(), "verifySig", verifySig)
		return
	}

	// Mark as received from this peer (don't send back)
	r.markSentToPeer(src.ID(), block.Hash)

	localHeight := r.stateV2.LatestHeight()

	// Try apply if it's the next block (height + parent match)
	if r.isNextBlock(block) {
		if err := r.applyBlock(block, verifySig); err != nil {
			r.logger.Error("Apply failed, caching", "number", block.Number, "err", err)
			r.pendingCache.Add(block, uint64(localHeight))
		}
	} else {
		// Cache all other blocks (future or past for potential reorg)
		r.pendingCache.Add(block, uint64(localHeight))
	}

	// Gossip the latest block to other peers
	if verifySig {
		r.gossipBlock(block, src.ID())
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
		if err := r.applyBlock(block, false); err != nil { // no signature verification
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
	if gap <= 0 {
		return
	}

	// Request missing blocks (limited to smallGapThreshold per cycle to avoid spam)
	end := localHeight + int64(smallGapThreshold)
	if end > maxPeerHeight {
		end = maxPeerHeight
	}
	r.requestMissingBlocks(localHeight+1, end)
}

// requestMissingBlocks requests blocks in range [start, end] from peers
func (r *BlockBroadcastReactor) requestMissingBlocks(start, end int64) {
	peers := r.Switch.Peers().List()
	if len(peers) == 0 {
		return
	}

	for height := start; height <= end; height++ {
		// Find a peer that has this height
		peer := r.findPeerWithHeight(peers, height)
		r.logger.Debug("Finding peer with height", "height", height, "peer", peer.ID())
		if peer == nil {
			continue
		}

		msg := &bcproto.BlockRequest{Height: height}
		bz, err := encodeMsg(msg)
		if err != nil {
			r.logger.Error("Failed to encode BlockRequest", "height", height, "err", err)
			continue
		}
		r.logger.Info("Requesting block", "height", height, "peer", peer.ID())
		if !peer.TrySend(SequencerSyncChannel, bz) {
			r.logger.Error("Failed to send BlockRequest (TrySend failed)", "height", height, "peer", peer.ID())
		}
	}
}

// findPeerWithHeight finds a random peer that has the given height.
// Uses random start index to avoid always hitting the same peer.
func (r *BlockBroadcastReactor) findPeerWithHeight(peers []p2p.Peer, height int64) p2p.Peer {
	n := len(peers)
	if n == 0 {
		return nil
	}

	// Random start, then wrap around
	start := rand.Intn(n)
	for i := 0; i < n; i++ {
		peer := peers[(start+i)%n]
		if r.pool.GetPeerHeight(peer.ID()) >= height {
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

// applyBlock: verify and apply a block atomically
// Thread-safe: uses mutex to ensure sequential block application
// verifySig: true for broadcast channel blocks, false for sync channel blocks
func (r *BlockBroadcastReactor) applyBlock(block *BlockV2, verifySig bool) error {
	r.applyMtx.Lock()
	defer r.applyMtx.Unlock()

	// Verify signature only for broadcast channel
	if verifySig && !r.verifySignature(block) {
		return fmt.Errorf("invalid signature")
	}

	// Verify parent
	currentBlock := r.stateV2.LatestBlock()
	if currentBlock != nil && block.ParentHash != currentBlock.Hash {
		return fmt.Errorf("parent mismatch")
	}

	// Update state via stateV2 (unified entry point)
	if err := r.stateV2.ApplyBlock(block); err != nil {
		return err
	}

	// Add to recent blocks
	r.recentBlocks.Add(block)

	r.logger.Info("Applied block", "number", block.Number, "verifySig", verifySig)
	return nil
}

func (r *BlockBroadcastReactor) verifySignature(block *BlockV2) bool {
	if len(block.Signature) == 0 {
		r.logger.Error("Signature verification failed: empty signature", "block", block.Number)
		return false
	}
	pubKey, err := crypto.SigToPub(block.Hash.Bytes(), block.Signature)
	if err != nil {
		r.logger.Error("Signature verification failed: SigToPub error", "block", block.Number, "err", err)
		return false
	}
	recoveredAddr := crypto.PubkeyToAddress(*pubKey)
	expectedAddr := upgrade.SequencerAddress
	if !upgrade.IsSequencer(recoveredAddr) {
		r.logger.Error("Signature verification failed: address mismatch",
			"block", block.Number,
			"recovered", recoveredAddr.Hex(),
			"expected", expectedAddr.Hex())
		return false
	}
	return true
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
		Block: BlockV2ToProto(block),
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

	resp := &bcproto.BlockResponseV2{
		Block: BlockV2ToProto(block),
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
		Block: BlockV2ToProto(block),
	}
	bz, err := encodeMsg(resp)
	if err != nil {
		r.logger.Error("Failed to encode BlockResponseV2 for broadcast", "number", block.Number, "err", err)
		return
	}
	r.Switch.Broadcast(BlockBroadcastChannel, bz)
	r.logger.Info("Broadcast block", "number", block.Number, "hash", block.Hash.Hex())
}

// ============================================================================
// Proto Conversion
// ============================================================================

func BlockV2ToProto(block *BlockV2) *seqproto.BlockV2 {
	var baseFee []byte
	if block.BaseFee != nil {
		baseFee = block.BaseFee.Bytes()
	}
	return &seqproto.BlockV2{
		ParentHash:         block.ParentHash.Bytes(),
		Miner:              block.Miner.Bytes(),
		Number:             block.Number,
		GasLimit:           block.GasLimit,
		BaseFee:            baseFee,
		Timestamp:          block.Timestamp,
		Transactions:       block.Transactions,
		StateRoot:          block.StateRoot.Bytes(),
		GasUsed:            block.GasUsed,
		ReceiptRoot:        block.ReceiptRoot.Bytes(),
		LogsBloom:          block.LogsBloom,
		WithdrawTrieRoot:   block.WithdrawTrieRoot.Bytes(),
		NextL1MessageIndex: block.NextL1MessageIndex,
		Hash:               block.Hash.Bytes(),
		Signature:          block.Signature,
	}
}

func ProtoToBlockV2(pb *seqproto.BlockV2) *BlockV2 {
	baseFee := new(big.Int)
	if len(pb.BaseFee) > 0 {
		baseFee.SetBytes(pb.BaseFee)
	}
	return &BlockV2{
		ParentHash:         common.BytesToHash(pb.ParentHash),
		Miner:              common.BytesToAddress(pb.Miner),
		Number:             pb.Number,
		GasLimit:           pb.GasLimit,
		BaseFee:            baseFee,
		Timestamp:          pb.Timestamp,
		Transactions:       pb.Transactions,
		StateRoot:          common.BytesToHash(pb.StateRoot),
		GasUsed:            pb.GasUsed,
		ReceiptRoot:        common.BytesToHash(pb.ReceiptRoot),
		LogsBloom:          pb.LogsBloom,
		WithdrawTrieRoot:   common.BytesToHash(pb.WithdrawTrieRoot),
		NextL1MessageIndex: pb.NextL1MessageIndex,
		Hash:               common.BytesToHash(pb.Hash),
		Signature:          pb.Signature,
	}
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
