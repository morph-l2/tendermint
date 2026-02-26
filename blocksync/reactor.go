package blocksync

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/tendermint/tendermint/l2node"
	"github.com/tendermint/tendermint/libs/log"
	"github.com/tendermint/tendermint/p2p"
	bcproto "github.com/tendermint/tendermint/proto/tendermint/blocksync"
	sm "github.com/tendermint/tendermint/state"
	"github.com/tendermint/tendermint/store"
	"github.com/tendermint/tendermint/types"
	"github.com/tendermint/tendermint/upgrade"
)

const (
	// BlocksyncChannel is a channel for blocks and status updates (`BlockStore` height)
	BlocksyncChannel = byte(0x40)

	trySyncIntervalMS = 10

	// stop syncing when last block's time is
	// within this much of the system time.
	// stopSyncingDurationMinutes = 10

	// ask for best height every 10s
	statusUpdateIntervalSeconds = 10
	// check if we should switch to consensus reactor
	switchToConsensusIntervalSeconds = 1
)

type consensusReactor interface {
	// for when we switch from blockchain reactor and block sync to
	// the consensus machine
	SwitchToConsensus(state sm.State, skipWAL bool)
}

type sequencerReactor interface {
	// for when we switch from blockchain reactor to sequencer mode
	StartSequencerRoutines() error
}

// SequencerState interface for accessing sequencer state (avoids import cycle)
type SequencerState interface {
	LatestHeight() int64
	ApplyBlock(block *types.BlockV2) error
}

type peerError struct {
	err    error
	peerID p2p.ID
}

func (e peerError) Error() string {
	return fmt.Sprintf("error with peer %v: %s", e.peerID, e.err.Error())
}

// Reactor handles long-term catchup syncing.
type Reactor struct {
	p2p.BaseReactor

	l2Node  l2node.L2Node  // Unified interface for both PBFT and sequencer mode
	stateV2 SequencerState // Sequencer state for post-upgrade sync

	// immutable
	initialState sm.State

	blockExec *sm.BlockExecutor
	store     *store.BlockStore
	pool      *BlockPool
	blockSync bool

	requestsCh <-chan BlockRequest
	errorsCh   <-chan peerError
}

// NewReactor returns new reactor instance.
func NewReactor(
	l2Node l2node.L2Node,
	state sm.State,
	blockExec *sm.BlockExecutor,
	store *store.BlockStore,
	blockSync bool,
	stateV2 SequencerState,
) *Reactor {

	if state.LastBlockHeight != store.Height() {
		panic(fmt.Sprintf("state (%v) and store (%v) height mismatch", state.LastBlockHeight, store.Height()))
	}

	requestsCh := make(chan BlockRequest, maxTotalRequesters)

	const capacity = 1000                      // must be bigger than peers count
	errorsCh := make(chan peerError, capacity) // so we don't block in #Receive#pool.AddBlock

	// Determine start height: max of state height and l2node height (for post-upgrade)
	startHeight := store.Height() + 1
	if startHeight == 1 {
		startHeight = state.InitialHeight
	}

	// Check l2node for latest block (may be ahead after upgrade)
	if l2Node != nil {
		if latestBlock, err := l2Node.GetLatestBlockV2(); err == nil && latestBlock != nil {
			l2Height := int64(latestBlock.Number)
			if l2Height >= startHeight {
				startHeight = l2Height + 1
			}
		}
	}

	pool := NewBlockPool(startHeight, requestsCh, errorsCh)

	bcR := &Reactor{
		l2Node:       l2Node,
		stateV2:      stateV2,
		initialState: state,
		blockExec:    blockExec,
		store:        store,
		pool:         pool,
		blockSync:    blockSync,
		requestsCh:   requestsCh,
		errorsCh:     errorsCh,
	}
	bcR.BaseReactor = *p2p.NewBaseReactor("Reactor", bcR)
	return bcR
}

// SetLogger implements service.Service by setting the logger on reactor and pool.
func (bcR *Reactor) SetLogger(l log.Logger) {
	bcR.BaseService.Logger = l
	bcR.pool.Logger = l
}

// SetStateV2 sets the sequencer state (called after upgrade).
func (bcR *Reactor) SetStateV2(stateV2 SequencerState) {
	bcR.stateV2 = stateV2
}

// Pool returns the block pool for broadcast reactor to check peer heights.
func (bcR *Reactor) Pool() *BlockPool {
	return bcR.pool
}

// OnStart implements service.Service.
func (bcR *Reactor) OnStart() error {
	if bcR.blockSync {
		err := bcR.pool.Start()
		if err != nil {
			return err
		}
		go bcR.poolRoutine(false)
	}
	return nil
}

// SwitchToBlockSync is called by the state sync reactor when switching to block sync.
func (bcR *Reactor) SwitchToBlockSync(state sm.State) error {
	bcR.blockSync = true
	bcR.initialState = state

	bcR.pool.height = state.LastBlockHeight + 1
	err := bcR.pool.Start()
	if err != nil {
		return err
	}
	go bcR.poolRoutine(true)
	return nil
}

// OnStop implements service.Service.
func (bcR *Reactor) OnStop() {
	if bcR.blockSync {
		if err := bcR.pool.Stop(); err != nil {
			bcR.Logger.Error("Error stopping pool", "err", err)
		}
	}
}

// GetChannels implements Reactor
func (bcR *Reactor) GetChannels() []*p2p.ChannelDescriptor {
	return []*p2p.ChannelDescriptor{
		{
			ID:                  BlocksyncChannel,
			Priority:            5,
			SendQueueCapacity:   1000,
			RecvBufferCapacity:  50 * 4096,
			RecvMessageCapacity: MaxMsgSize,
		},
	}
}

// AddPeer implements Reactor by sending our state to peer.
func (bcR *Reactor) AddPeer(peer p2p.Peer) {
	msgBytes, err := EncodeMsg(&bcproto.StatusResponse{
		Base:   bcR.store.Base(),
		Height: bcR.getHeight()})
	if err != nil {
		bcR.Logger.Error("could not convert msg to protobuf", "err", err)
		return
	}

	peer.Send(BlocksyncChannel, msgBytes)
	// it's OK if send fails. will try later in poolRoutine

	// peer is added to the pool once we receive the first
	// bcStatusResponseMessage from the peer and call pool.SetPeerRange
}

// RemovePeer implements Reactor by removing peer from the pool.
func (bcR *Reactor) RemovePeer(peer p2p.Peer, reason interface{}) {
	bcR.pool.RemovePeer(peer.ID())
}

// respondToPeer loads a block and sends it to the requesting peer,
// if we have it. Otherwise, we'll respond saying we don't have it.
func (bcR *Reactor) respondToPeer(msg *bcproto.BlockRequest,
	src p2p.Peer) (queued bool) {

	// Check if already upgraded
	if upgrade.IsUpgraded(msg.Height) {
		return bcR.respondToPeerV2(msg, src)
	}

	block := bcR.store.LoadBlock(msg.Height)
	if block != nil {
		bl, err := block.ToProto()
		if err != nil {
			bcR.Logger.Error("could not convert msg to protobuf", "err", err)
			return false
		}

		msgBytes, err := EncodeMsg(&bcproto.BlockResponse{Block: bl})
		if err != nil {
			bcR.Logger.Error("could not marshal msg", "err", err)
			return false
		}

		return src.TrySend(BlocksyncChannel, msgBytes)
	}

	bcR.Logger.Info("Peer asking for a block we don't have", "src", src, "height", msg.Height)

	msgBytes, err := EncodeMsg(&bcproto.NoBlockResponse{Height: msg.Height})
	if err != nil {
		bcR.Logger.Error("could not convert msg to protobuf", "err", err)
		return false
	}

	return src.TrySend(BlocksyncChannel, msgBytes)
}

// respondToPeerV2 handles block requests after the sequencer upgrade.
// It retrieves blocks from geth instead of the local blockStore.
func (bcR *Reactor) respondToPeerV2(msg *bcproto.BlockRequest, src p2p.Peer) bool {
	// Get block from geth using unified l2Node interface
	blockData, err := bcR.l2Node.GetBlockByNumber(uint64(msg.Height))
	if err != nil {
		bcR.Logger.Error("Failed to get block from geth", "height", msg.Height, "err", err)

		// Send NoBlockResponse
		msgBytes, encErr := EncodeMsg(&bcproto.NoBlockResponse{Height: msg.Height})
		if encErr != nil {
			bcR.Logger.Error("could not convert msg to protobuf", "err", encErr)
			return false
		}
		return src.TrySend(BlocksyncChannel, msgBytes)
	}

	bcR.Logger.Debug("respondToPeerV2: got block from geth",
		"height", msg.Height,
		"hash", blockData.Hash.Hex())

	// Convert to proto and send using BlockResponseV2
	blockV2Proto := types.BlockV2ToProto(blockData)
	msgBytes, err := EncodeMsg(&bcproto.BlockResponseV2{
		Block: blockV2Proto,
	})
	if err != nil {
		bcR.Logger.Error("could not encode BlockV2 response", "err", err)
		return false
	}
	return src.TrySend(BlocksyncChannel, msgBytes)
}

// L2Node returns the L2Node interface for use by sequencer mode.
func (bcR *Reactor) L2Node() l2node.L2Node {
	return bcR.l2Node
}

// syncBlockV2 handles syncing a single BlockV2 in sequencer mode.
// No signature verification during sync - only broadcast channel verifies signatures.
// Returns true if sync was successful, false if there was an error (already handled).
func (bcR *Reactor) syncBlockV2(block types.SyncableBlock, blocksSynced *uint64, lastRate *float64, lastHundred *time.Time) bool {
	blockV2, ok := block.(*types.BlockV2)
	if !ok {
		bcR.Logger.Error("Expected BlockV2 after upgrade", "height", block.GetHeight())
		bcR.pool.RedoRequest(block.GetHeight())
		return false
	}

	// Apply BlockV2 via stateV2 (no signature verification during sync)
	if err := bcR.stateV2.ApplyBlock(blockV2); err != nil {
		bcR.Logger.Error("Failed to apply BlockV2", "height", blockV2.Number, "err", err)
		bcR.pool.RedoRequest(blockV2.GetHeight())
		return false
	}

	bcR.pool.PopRequest()
	*blocksSynced++

	if *blocksSynced%100 == 0 {
		*lastRate = 0.9*(*lastRate) + 0.1*(100/time.Since(*lastHundred).Seconds())
		bcR.Logger.Info(
			"BlockV2 Sync Rate",
			"height", bcR.pool.height,
			"max_peer_height", bcR.pool.MaxPeerHeight(),
			"blocks/s", *lastRate,
		)
		*lastHundred = time.Now()
	}
	return true
}

// Receive implements Reactor by handling 4 types of messages (look below).
func (bcR *Reactor) Receive(chID byte, src p2p.Peer, msgBytes []byte) {
	msg, err := DecodeMsg(msgBytes)
	if err != nil {
		bcR.Logger.Error("Error decoding message", "src", src, "chId", chID, "err", err)
		bcR.Switch.StopPeerForError(src, err)
		return
	}

	if err = ValidateMsg(msg); err != nil {
		bcR.Logger.Error("Peer sent us invalid msg", "peer", src, "msg", msg, "err", err)
		bcR.Switch.StopPeerForError(src, err)
		return
	}

	bcR.Logger.Debug("Receive", "src", src, "chID", chID, "msg", msg)

	switch msg := msg.(type) {
	case *bcproto.BlockRequest:
		bcR.respondToPeer(msg, src)
	case *bcproto.BlockResponse:
		bi, err := types.BlockFromProto(msg.Block)
		if err != nil {
			bcR.Logger.Error("Block content is invalid", "err", err)
			return
		}
		bcR.pool.AddBlock(src.ID(), bi, len(msgBytes))
	case *bcproto.BlockResponseV2:
		blockV2, err := types.BlockV2FromProto(msg.Block)
		if err != nil {
			bcR.Logger.Error("BlockV2 content is invalid", "err", err)
			return
		}
		bcR.pool.AddBlock(src.ID(), blockV2, len(msgBytes))
	case *bcproto.StatusRequest:
		// Send peer our state.
		msgBytes, err := EncodeMsg(&bcproto.StatusResponse{
			Height: bcR.getHeight(),
			Base:   bcR.store.Base(),
		})
		if err != nil {
			bcR.Logger.Error("could not convert msg to protobut", "err", err)
			return
		}
		src.TrySend(BlocksyncChannel, msgBytes)
	case *bcproto.StatusResponse:
		// Got a peer status. Unverified.
		bcR.Logger.Debug("SetPeerRange", "peer", src.ID(), "base", msg.Base, "height", msg.Height)
		bcR.pool.SetPeerRange(src.ID(), msg.Base, msg.Height)
	case *bcproto.NoBlockResponse:
		bcR.Logger.Debug("Peer does not have requested block", "peer", src, "height", msg.Height)
	default:
		bcR.Logger.Error(fmt.Sprintf("Unknown message type %v", reflect.TypeOf(msg)))
	}
}

// Handle messages from the poolReactor telling the reactor what to do.
// NOTE: Don't sleep in the FOR_LOOP or otherwise slow it down!
func (bcR *Reactor) poolRoutine(stateSynced bool) {
	// optimize: try to get peer status immediately after start
	bcR.BroadcastStatusRequest()

	trySyncTicker := time.NewTicker(trySyncIntervalMS * time.Millisecond)
	defer trySyncTicker.Stop()

	statusUpdateTicker := time.NewTicker(statusUpdateIntervalSeconds * time.Second)
	// no longer stop the ticker, reuse it for sequencer mode status updates
	//defer statusUpdateTicker.Stop()

	switchToConsensusTicker := time.NewTicker(switchToConsensusIntervalSeconds * time.Second)
	defer switchToConsensusTicker.Stop()

	blocksSynced := uint64(0)

	chainID := bcR.initialState.ChainID
	state := bcR.initialState

	lastHundred := time.Now()
	lastRate := 0.0

	didProcessCh := make(chan struct{}, 1)

	go func() {
		for {
			select {
			case <-bcR.Quit():
				return
			// Note: removed `case <-bcR.pool.Quit(): return` to keep peer status updates for stateV2
			// running after pool.Stop(). pool.SetPeerRange works regardless of pool state.
			//case <-bcR.pool.Quit():
			//	return
			case request := <-bcR.requestsCh:
				peer := bcR.Switch.Peers().Get(request.PeerID)
				if peer == nil {
					continue
				}
				msgBytes, err := EncodeMsg(&bcproto.BlockRequest{Height: request.Height})
				if err != nil {
					bcR.Logger.Error("could not convert msg to proto", "err", err)
					continue
				}
				queued := peer.TrySend(BlocksyncChannel, msgBytes)
				if !queued {
					bcR.Logger.Debug("Send queue is full, drop block request", "peer", peer.ID(), "height", request.Height)
				}
			case err := <-bcR.errorsCh:
				peer := bcR.Switch.Peers().Get(err.peerID)
				if peer != nil {
					bcR.Switch.StopPeerForError(peer, err)
				}

			case <-statusUpdateTicker.C:
				// ask for status updates
				go bcR.BroadcastStatusRequest() //nolint: errcheck
			}
		}
	}()

FOR_LOOP:
	for {
		select {
		case <-switchToConsensusTicker.C:
			height, numPending, lenRequesters := bcR.pool.GetStatus()
			outbound, inbound, _ := bcR.Switch.NumPeers()
			bcR.Logger.Debug(
				"Consensus ticker",
				"numPending", numPending,
				"total", lenRequesters,
				"outbound", outbound,
				"inbound", inbound,
			)

			if bcR.pool.IsCaughtUp() {
				// Stop pool and tickers (peer status updates continue in background goroutine)
				bcR.Logger.Info("Caught up, stopping pool", "height", height)
				if err := bcR.pool.Stop(); err != nil {
					bcR.Logger.Error("Error stopping pool", "err", err)
				}

				if upgrade.IsUpgraded(height) {
					// Sequencer mode
					bcR.Logger.Info("Switching to sequencer mode", "height", height)
					seqR, ok := bcR.Switch.Reactor("SEQUENCER").(sequencerReactor)
					if ok {
						if err := seqR.StartSequencerRoutines(); err != nil {
							bcR.Logger.Error("Failed to start sequencer mode", "err", err)
						}
					}
				} else {
					// PBFT mode
					bcR.Logger.Info("Switching to consensus reactor", "height", height)
					conR, ok := bcR.Switch.Reactor("CONSENSUS").(consensusReactor)
					if ok {
						conR.SwitchToConsensus(state, blocksSynced > 0 || stateSynced)
					}
				}
				break FOR_LOOP
			}

		case <-trySyncTicker.C: // chan time
			select {
			case didProcessCh <- struct{}{}:
			default:
			}

		case <-didProcessCh:
			// NOTE: It is a subtle mistake to process more than a single block
			// at a time (e.g. 10) here, because we only TrySend 1 request per
			// loop.  The ratio mismatch can result in starving of blocks, a
			// sudden burst of requests and responses, and repeat.
			// Consequently, it is better to split these routines rather than
			// coupling them as it's written here.  TODO uncouple from request
			// routine.

			// See if there are any blocks to sync.
			firstSync, secondSync := bcR.pool.PeekTwoBlocks()
			// bcR.Logger.Info("TrySync peeked", "first", first, "second", second)
			if firstSync == nil || secondSync == nil {
				// We need both to sync the first block.
				continue FOR_LOOP
			} else {
				// Try again quickly next loop.
				didProcessCh <- struct{}{}
			}

			if firstSync.GetHeight()+1 == upgrade.UpgradeBlockHeight {
				if err := bcR.handleTheLastTMBlock(state, firstSync); err != nil {
					bcR.Logger.Error("Error in apply last tendermint block, ", "err", err)
					bcR.pool.PopRequest()
					blocksSynced++
					continue FOR_LOOP
				}
			}

			// Check if we're in sequencer mode (after upgrade)
			if upgrade.IsUpgraded(firstSync.GetHeight()) {
				bcR.syncBlockV2(firstSync, &blocksSynced, &lastRate, &lastHundred)
				continue FOR_LOOP
			}

			// PBFT mode: type assert to *types.Block
			first, second := firstSync.(*types.Block), secondSync.(*types.Block)

			firstParts, err := first.MakePartSet(types.BlockPartSizeBytes)
			if err != nil {
				bcR.Logger.Error(
					"failed to make ",
					"height", first.Height,
					"err", err.Error(),
				)
				break FOR_LOOP
			}
			firstPartSetHeader := firstParts.Header()
			firstID := types.BlockID{Hash: first.Hash(), BatchHash: first.BatchHash, PartSetHeader: firstPartSetHeader}
			// Finally, verify the first block using the second's commit
			// NOTE: we can probably make this more efficient, but note that calling
			// first.Hash() doesn't verify the tx contents, so MakePartSet() is
			// currently necessary.
			if err = state.Validators.VerifyCommitLight(chainID, firstID, first.Height, second.LastCommit); err == nil {
				// validate the block before we persist it
				err = bcR.blockExec.ValidateBlock(state, first)
			}

			// make sure the block has valid batchHash and batchHeader, and has the enough valid BLS signatures if it is a batch point
			if err == nil {
				err = func() error {
					// check the correctness of the relation of batchHash and batchHeader
					if len(first.L2BatchHeader) > 0 {
						batchHash, hashErr := bcR.l2Node.BatchHash(first.L2BatchHeader)
						if hashErr != nil {
							return hashErr
						}
						if !bytes.Equal(first.BatchHash, batchHash) {
							return fmt.Errorf("wrong batchHash. expectedHash: %x, actualHash: %x, batchHeader: %x", batchHash, first.BatchHash, first.L2BatchHeader)
						}
					} else if len(first.BatchHash) > 0 {
						return errors.New("batch hash can not exist when batchHeader is empty")
					}

					blsDatas, err := l2node.GetBLSDatas(second.LastCommit, state.Validators)
					if err != nil {
						return err
					}
					var validVotingPowers int64
					if len(blsDatas) > 0 {
						if len(first.BatchHash) == 0 {
							return errors.New("should not have bls signatures when batchHash is empty")
						}
						for _, blsData := range blsDatas {
							// todo currently can not ensure the l2node has the corresponding bls public key of the signer
							//valid, err := bcR.l2Node.VerifySignature(blsData.Signer, first.BatchHash, blsData.Signature)
							//if err != nil {
							//	return err
							//}
							//if valid {
							//	validVotingPowers += blsData.VotingPower
							//}
							validVotingPowers += blsData.VotingPower
						}
						quorum := state.Validators.TotalVotingPower()*2/3 + 1
						if validVotingPowers < quorum {
							return fmt.Errorf("not enough votingPowers of valid bls signature. quorum: %d, valid votingPower: %d", quorum, validVotingPowers)
						}
					} else if len(first.BatchHash) > 0 {
						return errors.New("must have bls signatures when batchHash is not empty")
					}
					return nil
				}()
			}

			if err != nil {
				bcR.Logger.Error("Error in validation", "err", err)
				peerID := bcR.pool.RedoRequest(first.Height)
				peer := bcR.Switch.Peers().Get(peerID)
				if peer != nil {
					// NOTE: we've already removed the peer's request, but we
					// still need to clean up the rest.
					bcR.Switch.StopPeerForError(peer, fmt.Errorf("Reactor validation error: %v", err))
				}
				peerID2 := bcR.pool.RedoRequest(second.Height)
				peer2 := bcR.Switch.Peers().Get(peerID2)
				if peer2 != nil && peer2 != peer {
					// NOTE: we've already removed the peer's request, but we
					// still need to clean up the rest.
					bcR.Switch.StopPeerForError(peer2, fmt.Errorf("Reactor validation error: %v", err))
				}
				continue FOR_LOOP
			}

			bcR.pool.PopRequest()

			// TODO: batch saves so we dont persist to disk every block
			bcR.store.SaveBlock(first, firstParts, second.LastCommit)

			// TODO: same thing for app - but we would need a way to
			// get the hash without persisting the state
			state, _, err = bcR.blockExec.ApplyBlock(
				state,
				firstID,
				first,
				nil,
			)
			if err != nil {
				// TODO This is bad, are we zombie?
				panic(fmt.Sprintf("Failed to process committed block (%d:%X): %v", first.Height, first.Hash(), err))
			}

			blocksSynced++

			if blocksSynced%100 == 0 {
				lastRate = 0.9*lastRate + 0.1*(100/time.Since(lastHundred).Seconds())
				bcR.Logger.Info(
					"Block Sync Rate",
					"height", bcR.pool.height,
					"max_peer_height", bcR.pool.MaxPeerHeight(),
					"blocks/s", lastRate,
				)
				lastHundred = time.Now()
			}

			continue FOR_LOOP

		case <-bcR.Quit():
			break FOR_LOOP
		}
	}
}

// BroadcastStatusRequest broadcasts `BlockStore` base and height.
func (bcR *Reactor) BroadcastStatusRequest() error {
	bm, err := EncodeMsg(&bcproto.StatusRequest{})
	if err != nil {
		bcR.Logger.Error("could not convert msg to proto", "err", err)
		return fmt.Errorf("could not convert msg to proto: %w", err)
	}

	bcR.Switch.Broadcast(BlocksyncChannel, bm)

	return nil
}

// just skip the last tendermint block commit check.
// TODO: consider add the commit check in the future or using batch derivation reorg
func (bcR *Reactor) handleTheLastTMBlock(state sm.State, lastSyncable types.SyncableBlock) error {
	last := lastSyncable.(*types.Block)
	lastParts, err := last.MakePartSet(types.BlockPartSizeBytes)
	if err != nil {
		bcR.Logger.Error(
			"failed to make ",
			"height", last.Height,
			"err", err.Error(),
		)
		return err
	}

	nilCommit := &types.Commit{Height: last.GetHeight()}
	bcR.store.SaveBlock(last, lastParts, nilCommit)
	lastPartSetHeader := lastParts.Header()
	lastID := types.BlockID{Hash: last.Hash(), BatchHash: last.BatchHash, PartSetHeader: lastPartSetHeader}

	// TODO: same thing for app - but we would need a way to
	// get the hash without persisting the state
	state, _, err = bcR.blockExec.ApplyBlock(
		state,
		lastID,
		last,
		nil,
	)
	if err != nil {
		// TODO This is bad, are we zombie?
		panic(fmt.Sprintf("Failed to process committed block (%d:%X): %v", last.Height, last.Hash(), err))
	}

	return nil
}

func (bcR *Reactor) getHeight() int64 {
	height := bcR.store.Height()
	// In sequencer mode, get height directly from l2Node (geth)
	if bcR.l2Node != nil && upgrade.IsUpgraded(height+1) {
		if l2Block, err := bcR.l2Node.GetLatestBlockV2(); err == nil && l2Block != nil {
			bcR.Logger.Debug("StatusRequest", "l2Height", l2Block.Number)
			if l2Height := int64(l2Block.Number); l2Height > height {
				height = l2Height
			}
		}
	}
	return height
}
