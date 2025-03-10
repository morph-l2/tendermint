package state

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	abci "github.com/tendermint/tendermint/abci/types"
	cfg "github.com/tendermint/tendermint/config"
	"github.com/tendermint/tendermint/crypto/ed25519"
	cryptoenc "github.com/tendermint/tendermint/crypto/encoding"
	"github.com/tendermint/tendermint/l2node"
	"github.com/tendermint/tendermint/libs/fail"
	"github.com/tendermint/tendermint/libs/log"
	tmstate "github.com/tendermint/tendermint/proto/tendermint/state"
	tmproto "github.com/tendermint/tendermint/proto/tendermint/types"
	"github.com/tendermint/tendermint/proxy"
	"github.com/tendermint/tendermint/types"
)

//-----------------------------------------------------------------------------
// BlockExecutor handles block execution and state updates.
// It exposes ApplyBlock(), which validates & executes the block, updates state w/ ABCI responses,
// then commits and saves state.

// BlockExecutor provides the context and accessories for properly executing a block.
type BlockExecutor struct {
	// save state, validators, consensus params, abci responses here
	store Store

	// execute the app against this
	proxyApp proxy.AppConnConsensus

	// execute l2 tx against this
	l2Node l2node.L2Node

	// events
	eventBus types.BlockEventPublisher

	notifier *l2node.Notifier

	evpool EvidencePool

	logger log.Logger

	metrics *Metrics
}

type BlockExecutorOption func(executor *BlockExecutor)

func BlockExecutorWithMetrics(metrics *Metrics) BlockExecutorOption {
	return func(blockExec *BlockExecutor) {
		blockExec.metrics = metrics
	}
}

// NewBlockExecutor returns a new BlockExecutor with a NopEventBus.
// Call SetEventBus to provide one.
func NewBlockExecutor(
	stateStore Store,
	logger log.Logger,
	proxyApp proxy.AppConnConsensus,
	l2Node l2node.L2Node,
	notifier *l2node.Notifier,
	evpool EvidencePool,
	options ...BlockExecutorOption,
) *BlockExecutor {
	res := &BlockExecutor{
		store:    stateStore,
		proxyApp: proxyApp,
		l2Node:   l2Node,
		eventBus: types.NopEventBus{},
		notifier: notifier,
		evpool:   evpool,
		logger:   logger,
		metrics:  NopMetrics(),
	}

	for _, option := range options {
		option(res)
	}

	return res
}

func (blockExec *BlockExecutor) RequestBlockData(height int64, createEmptyBlocksInterval time.Duration) {
	blockExec.notifier.RequestBlockData(height, createEmptyBlocksInterval)
}

func (blockExec *BlockExecutor) Store() Store {
	return blockExec.store
}

// SetEventBus - sets the event bus for publishing block related events.
// If not called, it defaults to types.NopEventBus.
func (blockExec *BlockExecutor) SetEventBus(eventBus types.BlockEventPublisher) {
	blockExec.eventBus = eventBus
}

// CreateProposalBlock calls state.MakeBlock with evidence from the evpool.
// The max bytes must be big enough to fit the commit.
// Up to 1/10th of the block space is allcoated for maximum sized evidence.
// The rest is given to txs, up to the max gas.
//
// Contract: application will not return more bytes than are sent over the wire.
func (blockExec *BlockExecutor) CreateProposalBlock(
	l2Node l2node.L2Node,
	config *cfg.ConsensusConfig,
	height int64,
	state State,
	commit *types.Commit,
	proposerAddr []byte,
	decideBatchPoint decideBatchPointFunc,
) (
	*types.Block, error,
) {
	maxBytes := state.ConsensusParams.Block.MaxBytes
	// maxGas := state.ConsensusParams.Block.MaxGas

	evidence, evSize := blockExec.evpool.PendingEvidence(state.ConsensusParams.Evidence.MaxBytes)

	// Fetch a limited amount of valid txs
	maxDataBytes := types.MaxDataBytes(maxBytes, evSize, state.Validators.Size())

	var txs [][]byte
	var blockMeta []byte
	var err error
	if config.WaitForTxs() {
		blockData := blockExec.notifier.WaitForBlockData()
		if blockData != nil && blockData.Height == height {
			txs = blockData.Txs
			blockMeta = blockData.Meta
		} else {
			txs, blockMeta, _, err = l2Node.RequestBlockData(height)
			if err != nil {
				return nil, err
			}
		}
		blockExec.notifier.CleanBlockData()
	} else {
		txs, blockMeta, _, err = l2Node.RequestBlockData(height)
		if err != nil {
			return nil, err
		}
	}

	block := state.MakeBlock(height, l2node.ConvertBytesToTxs(txs), blockMeta, commit, evidence, proposerAddr, nil) // set 'decideBatchPoint' to nil here to prevent duplicated execution.

	localLastCommit := buildLastCommitInfo(block, blockExec.store, state.InitialHeight)
	rpp, err := blockExec.proxyApp.PrepareProposalSync(
		abci.RequestPrepareProposal{
			MaxTxBytes:         maxDataBytes,
			Txs:                block.Txs.ToSliceOfBytes(),
			LocalLastCommit:    extendedCommitInfo(localLastCommit),
			Misbehavior:        block.Evidence.Evidence.ToABCI(),
			Height:             block.Height,
			Time:               block.Time,
			NextValidatorsHash: block.NextValidatorsHash,
			ProposerAddress:    block.ProposerAddress,
		},
	)
	if err != nil {
		// Also, the App can simply skip any transaction that could cause any kind of trouble.
		// Either way, we cannot recover in a meaningful way, unless we skip proposing
		// this block, repair what caused the error and try again. Hence, we return an
		// error for now (the production code calling this function is expected to panic).
		return nil, err
	}

	txl := types.ToTxs(rpp.Txs)
	if err := txl.Validate(maxDataBytes); err != nil {
		return nil, err
	}

	return state.MakeBlock(height, txl, blockMeta, commit, evidence, proposerAddr, decideBatchPoint), nil
}

func (blockExec *BlockExecutor) ProcessProposal(
	block *types.Block,
	state State,
) (bool, error) {
	resp, err := blockExec.proxyApp.ProcessProposalSync(abci.RequestProcessProposal{
		Hash:               block.Header.Hash(),
		Height:             block.Header.Height,
		Time:               block.Header.Time,
		Txs:                block.Data.Txs.ToSliceOfBytes(),
		ProposedLastCommit: buildLastCommitInfo(block, blockExec.store, state.InitialHeight),
		Misbehavior:        block.Evidence.Evidence.ToABCI(),
		ProposerAddress:    block.ProposerAddress,
		NextValidatorsHash: block.NextValidatorsHash,
	})
	if err != nil {
		return false, ErrInvalidBlock(err)
	}
	if resp.IsStatusUnknown() {
		panic(fmt.Sprintf("ProcessProposal responded with status %s", resp.Status.String()))
	}

	return resp.IsAccepted(), nil
}

// ValidateBlock validates the given block against the given state.
// If the block is invalid, it returns an error.
// Validation does not mutate state, but does require historical information from the stateDB,
// ie. to verify evidence from a validator at an old height.
func (blockExec *BlockExecutor) ValidateBlock(state State, block *types.Block) error {
	if err := validateBlock(state, block); err != nil {
		return err
	}
	return blockExec.evpool.CheckEvidence(block.Evidence.Evidence)
}

// ApplyBlock validates the block against the state, executes it against the app,
// fires the relevant events, commits the app, and saves the new state and responses.
// It returns the new state and the block height to retain (pruning older blocks).
// It's the only function that needs to be called
// from outside this package to process and commit an entire block.
// It takes a blockID to avoid recomputing the parts hash.
func (blockExec *BlockExecutor) ApplyBlock(
	state State,
	blockID types.BlockID,
	block *types.Block,
	commit *types.Commit,
) (
	State, int64, error,
) {
	err := validateBlock(state, block)
	if err != nil {
		return state, 0, ErrInvalidBlock(err)
	}

	// deliver blocks at execution layer
	var consensusParamUpdates *tmproto.ConsensusParams
	var validatorUpdates []*types.Validator

	nextValidators := state.NextValidators.GetPubKeyBytesList()
	if blockExec.l2Node != nil {
		startTime := time.Now().UnixNano()
		nextBatchParams, nextValidatorSet, err := ExecBlockOnL2Node(blockExec.logger, blockExec.l2Node, block, state.Validators, commit)
		endTime := time.Now().UnixNano()
		blockExec.metrics.BlockProcessingTime.Observe(float64(endTime-startTime) / 1000000)
		if err != nil {
			return state, 0, err
		}

		consensusParamUpdates = blockExec.GetConsensusParamsUpdate(nextBatchParams, nil, nil, nil, nil)
		validatorUpdates = blockExec.GetValidatorUpdates(nextValidatorSet, nextValidators)
	}

	if len(validatorUpdates) > 0 {
		blockExec.logger.Debug("updates to validators", "updates", types.ValidatorListString(validatorUpdates))
		blockExec.metrics.ValidatorSetUpdates.Add(1)
	}
	if consensusParamUpdates != nil {
		blockExec.metrics.ConsensusParamUpdates.Add(1)
	}

	// Update the state with the block and responses.
	state, err = updateState(state, blockID, &block.Header, consensusParamUpdates, validatorUpdates)
	if err != nil {
		return state, 0, fmt.Errorf("commit failed for application: %v", err)
	}

	appHash, retainHeight, err := blockExec.Commit(state, block)
	if err != nil {
		return state, 0, fmt.Errorf("commit failed for application: %v", err)
	}

	// Update evpool with the latest state.
	blockExec.evpool.Update(state, block.Evidence.Evidence)

	fail.Fail() // XXX

	// Update the app hash and save the state.
	state.AppHash = appHash
	if err := blockExec.store.Save(state); err != nil {
		return state, 0, err
	}

	fail.Fail() // XXX

	// Events are fired after everything else.
	// NOTE: if we crash between Commit and Save, events wont be fired during replay
	// fireEvents(blockExec.logger, blockExec.eventBus, block, abciResponses, validatorUpdates)

	return state, retainHeight, nil
}

func (blockExec *BlockExecutor) GetConsensusParamsUpdate(
	nextBatchParams *tmproto.BatchParams,
	block *tmproto.BlockParams,
	evidence *tmproto.EvidenceParams,
	validator *tmproto.ValidatorParams,
	version *tmproto.VersionParams,
) *tmproto.ConsensusParams {
	if nextBatchParams == nil && block == nil && evidence == nil && validator == nil && version == nil {
		return nil
	}
	return &tmproto.ConsensusParams{
		Batch:     nextBatchParams,
		Block:     block,
		Evidence:  evidence,
		Validator: validator,
		Version:   version,
	}
}

func (blockExec *BlockExecutor) GetValidatorUpdates(
	nextValidatorSet [][]byte,
	validatorSet [][]byte,
) (
	validatorUpdates []*types.Validator,
) {
	// search new valdator
	for _, nVal := range nextValidatorSet {
		exist := false
		for _, val := range validatorSet {
			if bytes.Equal(nVal, val) {
				exist = true
				break
			}
		}
		if !exist {
			pubkey := ed25519.PubKey(nVal)
			validatorUpdates = append(
				validatorUpdates,
				&types.Validator{
					Address:          pubkey.Address(),
					PubKey:           pubkey,
					VotingPower:      1,
					ProposerPriority: 0,
				},
			)
		}
	}
	// search validator removed
	for _, val := range validatorSet {
		exist := false
		for _, nVal := range nextValidatorSet {
			if bytes.Equal(val, nVal) {
				exist = true
				break
			}
		}
		if !exist {
			pubkey := ed25519.PubKey(val)
			validatorUpdates = append(
				validatorUpdates,
				&types.Validator{
					Address:          pubkey.Address(),
					PubKey:           pubkey,
					VotingPower:      0,
					ProposerPriority: 0,
				},
			)
		}
	}
	return validatorUpdates
}

// It returns the result of calling abci.Commit (the AppHash) and the height to retain (if any).
func (blockExec *BlockExecutor) Commit(
	state State,
	block *types.Block,
) (
	[]byte, int64, error,
) {
	// Commit block, get hash back
	res, err := blockExec.proxyApp.CommitSync()
	if err != nil {
		blockExec.logger.Error("client error during proxyAppConn.CommitSync", "err", err)
		return nil, 0, err
	}

	// ResponseCommit has no error code - just data
	blockExec.logger.Info(
		"committed state",
		"height", block.Height,
		"num_txs", len(block.Txs),
		"app_hash", fmt.Sprintf("%X", res.Data),
	)

	return res.Data, res.RetainHeight, err
}

//---------------------------------------------------------
// Helper functions for executing blocks and updating state

func ExecBlockOnL2Node(logger log.Logger, l2Node l2node.L2Node, block *types.Block, validatorSet *types.ValidatorSet, commit *types.Commit) (*tmproto.BatchParams, [][]byte, error) {
	var validators [][]byte
	if validatorSet != nil {
		validators = validatorSet.GetPubKeyBytesList()
	}

	nextBatchParams, nextValidatorSet, err := l2Node.DeliverBlock(
		l2node.ConvertTxsToBytes(block.Data.Txs),
		block.L2BlockMeta,
		l2node.ConsensusData{
			ValidatorSet: validators,
			BatchHash:    block.BatchHash,
		},
	)
	if err != nil {
		logger.Error("failed to deliver block", "err", err, "height", block.Height)
		return nil, nil, err
	}

	// batch operation
	if commit != nil {
		blsDatas, err := l2node.GetBLSDatas(commit, validatorSet)
		if err != nil {
			panic(err)
		}
		if len(block.BatchHash) > 0 { // this is a batchPoint
			if err = l2Node.CommitBatch(block.L2BlockMeta, block.Txs, blsDatas); err != nil {
				logger.Error("failed to commit batch", "err", err, "height", block.Height)
				return nil, nil, err
			}
		} else {
			if err = l2Node.PackCurrentBlock(block.L2BlockMeta, block.Txs); err != nil {
				logger.Error("failed to pack current block", "err", err, "height", block.Height)
				return nil, nil, err
			}
		}
	}

	return nextBatchParams, nextValidatorSet, nil
}

// Executes block's transactions on proxyAppConn.
// Returns a list of transaction results and updates to the validator set
func execBlockOnProxyApp(
	logger log.Logger,
	proxyAppConn proxy.AppConnConsensus,
	block *types.Block,
	store Store,
	initialHeight int64,
) (*tmstate.ABCIResponses, error) {
	var validTxs, invalidTxs = 0, 0

	txIndex := 0
	abciResponses := new(tmstate.ABCIResponses)
	dtxs := make([]*abci.ResponseDeliverTx, len(block.Txs))
	abciResponses.DeliverTxs = dtxs

	// Execute transactions and get hash.
	proxyCb := func(req *abci.Request, res *abci.Response) {
		if r, ok := res.Value.(*abci.Response_DeliverTx); ok {
			// TODO: make use of res.Log
			// TODO: make use of this info
			// Blocks may include invalid txs.
			txRes := r.DeliverTx
			if txRes.Code == abci.CodeTypeOK {
				validTxs++
			} else {
				logger.Debug("invalid tx", "code", txRes.Code, "log", txRes.Log)
				invalidTxs++
			}

			abciResponses.DeliverTxs[txIndex] = txRes
			txIndex++
		}
	}
	proxyAppConn.SetResponseCallback(proxyCb)

	commitInfo := buildLastCommitInfo(block, store, initialHeight)

	// Begin block
	var err error
	pbh := block.Header.ToProto()
	if pbh == nil {
		return nil, errors.New("nil header")
	}

	abciResponses.BeginBlock, err = proxyAppConn.BeginBlockSync(abci.RequestBeginBlock{
		Hash:                block.Hash(),
		Header:              *pbh,
		LastCommitInfo:      commitInfo,
		ByzantineValidators: block.Evidence.Evidence.ToABCI(),
	})
	if err != nil {
		logger.Error("error in proxyAppConn.BeginBlock", "err", err)
		return nil, err
	}

	// run txs of block
	for _, tx := range block.Txs {
		proxyAppConn.DeliverTxAsync(abci.RequestDeliverTx{Tx: tx})
		if err := proxyAppConn.Error(); err != nil {
			return nil, err
		}
	}

	// End block.
	abciResponses.EndBlock, err = proxyAppConn.EndBlockSync(abci.RequestEndBlock{Height: block.Height})
	if err != nil {
		logger.Error("error in proxyAppConn.EndBlock", "err", err)
		return nil, err
	}

	logger.Info("executed block", "height", block.Height, "num_valid_txs", validTxs, "num_invalid_txs", invalidTxs)
	return abciResponses, nil
}

func buildLastCommitInfo(block *types.Block, store Store, initialHeight int64) abci.CommitInfo {
	if block.Height == initialHeight {
		// there is no last commit for the initial height.
		// return an empty value.
		return abci.CommitInfo{}
	}

	lastValSet, err := store.LoadValidators(block.Height - 1)
	if err != nil {
		panic(fmt.Errorf("failed to load validator set at height %d: %w", block.Height-1, err))
	}

	var (
		commitSize = block.LastCommit.Size()
		valSetLen  = len(lastValSet.Validators)
	)

	// ensure that the size of the validator set in the last commit matches
	// the size of the validator set in the state store.
	if commitSize != valSetLen {
		panic(fmt.Sprintf(
			"commit size (%d) doesn't match validator set length (%d) at height %d\n\n%v\n\n%v",
			commitSize, valSetLen, block.Height, block.LastCommit.Signatures, lastValSet.Validators,
		))
	}

	votes := make([]abci.VoteInfo, block.LastCommit.Size())
	for i, val := range lastValSet.Validators {
		commitSig := block.LastCommit.Signatures[i]
		votes[i] = abci.VoteInfo{
			Validator:       types.TM2PB.Validator(val),
			SignedLastBlock: commitSig.BlockIDFlag != types.BlockIDFlagAbsent,
		}
	}

	return abci.CommitInfo{
		Round: block.LastCommit.Round,
		Votes: votes,
	}
}

func extendedCommitInfo(c abci.CommitInfo) abci.ExtendedCommitInfo {
	vs := make([]abci.ExtendedVoteInfo, len(c.Votes))
	for i := range vs {
		vs[i] = abci.ExtendedVoteInfo{
			Validator:       c.Votes[i].Validator,
			SignedLastBlock: c.Votes[i].SignedLastBlock,
			/*
				TODO: Include vote extensions information when implementing vote extensions.
				VoteExtension:   []byte{},
			*/
		}
	}
	return abci.ExtendedCommitInfo{
		Round: c.Round,
		Votes: vs,
	}
}

func validateValidatorUpdates(abciUpdates []abci.ValidatorUpdate, params types.ValidatorParams) error {
	for _, valUpdate := range abciUpdates {
		if valUpdate.GetPower() < 0 {
			return fmt.Errorf("voting power can't be negative %v", valUpdate)
		} else if valUpdate.GetPower() == 0 {
			// continue, since this is deleting the validator, and thus there is no
			// pubkey to check
			continue
		}

		// Check if validator's pubkey matches an ABCI type in the consensus params
		pk, err := cryptoenc.PubKeyFromProto(valUpdate.PubKey)
		if err != nil {
			return err
		}

		if !types.IsValidPubkeyType(params, pk.Type()) {
			return fmt.Errorf("validator %v is using pubkey %s, which is unsupported for consensus",
				valUpdate, pk.Type())
		}
	}
	return nil
}

// updateState returns a new State updated according to the header and responses.
func updateState(
	state State,
	blockID types.BlockID,
	header *types.Header,
	consensusParamUpdates *tmproto.ConsensusParams,
	validatorUpdates []*types.Validator,
) (
	State, error,
) {
	// Copy the valset so we can apply changes from EndBlock
	// and update s.LastValidators and s.Validators.
	nValSet := state.NextValidators.Copy()

	lastHeightValsChanged := state.LastHeightValidatorsChanged
	if len(validatorUpdates) > 0 {
		if err := nValSet.UpdateWithChangeSet(validatorUpdates); err != nil {
			return state, fmt.Errorf("error changing validator set: %v", err)
		}
		// Change results from this height but only applies to the next next height.
		lastHeightValsChanged = header.Height + 1 + 1
	}

	// Update validator proposer priority and set state variables.
	nValSet.IncrementProposerPriority(1)

	nextParams := state.ConsensusParams
	lastHeightParamsChanged := state.LastHeightConsensusParamsChanged
	if consensusParamUpdates != nil {
		// NOTE: must not mutate s.ConsensusParams
		nextParams = state.ConsensusParams.Update(consensusParamUpdates)
		err := nextParams.ValidateBasic()
		if err != nil {
			return state, fmt.Errorf("error updating consensus params: %v", err)
		}

		state.Version.Consensus.App = nextParams.Version.App

		// Change results from this height but only applies to the next height.
		lastHeightParamsChanged = header.Height + 1
	}

	nextVersion := state.Version

	// NOTE: the AppHash has not been populated.
	// It will be filled on state.Save.
	return State{
		Version:                          nextVersion,
		ChainID:                          state.ChainID,
		InitialHeight:                    state.InitialHeight,
		LastBlockHeight:                  header.Height,
		LastBlockID:                      blockID,
		LastBlockTime:                    header.Time,
		NextValidators:                   nValSet,
		Validators:                       state.NextValidators.Copy(),
		LastValidators:                   state.Validators.Copy(),
		LastHeightValidatorsChanged:      lastHeightValsChanged,
		ConsensusParams:                  nextParams,
		LastHeightConsensusParamsChanged: lastHeightParamsChanged,
		LastResultsHash:                  nil,
		AppHash:                          state.AppHash,
	}, nil
}

// Fire NewBlock, NewBlockHeader.
// Fire TxEvent for every tx.
// NOTE: if Tendermint crashes before commit, some or all of these events may be published again.
func fireEvents(
	logger log.Logger,
	eventBus types.BlockEventPublisher,
	block *types.Block,
	abciResponses *tmstate.ABCIResponses,
	validatorUpdates []*types.Validator,
) {
	if err := eventBus.PublishEventNewBlock(types.EventDataNewBlock{
		Block:            block,
		ResultBeginBlock: *abciResponses.BeginBlock,
		ResultEndBlock:   *abciResponses.EndBlock,
	}); err != nil {
		logger.Error("failed publishing new block", "err", err)
	}

	if err := eventBus.PublishEventNewBlockHeader(types.EventDataNewBlockHeader{
		Header:           block.Header,
		NumTxs:           int64(len(block.Txs)),
		ResultBeginBlock: *abciResponses.BeginBlock,
		ResultEndBlock:   *abciResponses.EndBlock,
	}); err != nil {
		logger.Error("failed publishing new block header", "err", err)
	}

	if len(block.Evidence.Evidence) != 0 {
		for _, ev := range block.Evidence.Evidence {
			if err := eventBus.PublishEventNewEvidence(types.EventDataNewEvidence{
				Evidence: ev,
				Height:   block.Height,
			}); err != nil {
				logger.Error("failed publishing new evidence", "err", err)
			}
		}
	}

	for i, tx := range block.Data.Txs {
		if err := eventBus.PublishEventTx(types.EventDataTx{TxResult: abci.TxResult{
			Height: block.Height,
			Index:  uint32(i),
			Tx:     tx,
			Result: *(abciResponses.DeliverTxs[i]),
		}}); err != nil {
			logger.Error("failed publishing event TX", "err", err)
		}
	}

	if len(validatorUpdates) > 0 {
		if err := eventBus.PublishEventValidatorSetUpdates(
			types.EventDataValidatorSetUpdates{ValidatorUpdates: validatorUpdates}); err != nil {
			logger.Error("failed publishing event", "err", err)
		}
	}
}

//----------------------------------------------------------------------------------------------------
// Execute block without state. TODO: eliminate

// ExecCommitBlock executes and commits a block on the proxyApp without validating or mutating the state.
// It returns the application root hash (result of abci.Commit).
func ExecCommitBlock(
	appConnConsensus proxy.AppConnConsensus,
	block *types.Block,
	logger log.Logger,
	store Store,
	initialHeight int64,
) ([]byte, error) {
	_, err := execBlockOnProxyApp(logger, appConnConsensus, block, store, initialHeight)
	if err != nil {
		logger.Error("failed executing block on proxy app", "height", block.Height, "err", err)
		return nil, err
	}

	// Commit block, get hash back
	res, err := appConnConsensus.CommitSync()
	if err != nil {
		logger.Error("client error during proxyAppConn.CommitSync", "err", res)
		return nil, err
	}

	// ResponseCommit has no error or log, just data
	return res.Data, nil
}
