package state

import (
	"errors"
	"fmt"
	"path/filepath"

	cfg "github.com/tendermint/tendermint/config"
	"github.com/tendermint/tendermint/libs/os"
	"github.com/tendermint/tendermint/privval"
	tmstate "github.com/tendermint/tendermint/proto/tendermint/state"
	tmversion "github.com/tendermint/tendermint/proto/tendermint/version"
	"github.com/tendermint/tendermint/store"
	"github.com/tendermint/tendermint/types"
	"github.com/tendermint/tendermint/version"
	dbm "github.com/tendermint/tm-db"
)

func RewindTo(height int64, config *cfg.Config) error {
	// restore tendermint state
	blockStore, stateStore, err := loadStateAndBlockStore(config)
	if err != nil {
		return err
	}

	stateRewindTo, err := restoreStateFromBlock(blockStore, stateStore, height)
	if err != nil {
		return fmt.Errorf("failed to rewind state to %d: %w", height, err)
	}
	if err := stateStore.Save(*stateRewindTo); err != nil {
		fmt.Printf("failed to save state into stateStore, err: %v", err)
		return err
	}

	if _, err := blockStore.PruneBlocksSince(height + 1); err != nil {
		fmt.Printf("Failed to prune blocks, err: %v", err)
		return err
	}

	modifyPrivValidatorsFile(config, height)
	fmt.Printf("Rewind state to height %d \n", height)
	return nil
}

func restoreStateFromBlock(bs *store.BlockStore, ss Store, rewindHeight int64) (*State, error) {
	currentState, err := ss.Load() // current state
	if err != nil {
		return nil, err
	}
	if currentState.IsEmpty() {
		return nil, errors.New("no state found")
	}

	var (
		rewindBlock      *types.BlockMeta
		lastValidatorSet *types.ValidatorSet
		validatorSet     *types.ValidatorSet
		nextValidatorSet *types.ValidatorSet

		valChangeHeight    int64
		paramsChangeHeight int64
	)

	if currentState.LastBlockHeight == rewindHeight {
		fmt.Printf("the state is already at the height of %d \n", rewindHeight)
		return &currentState, nil
	}

	// rewindHeight is the `lastBlock` for the state we expect to restore
	if rewindBlock = bs.LoadBlockMeta(rewindHeight); rewindBlock == nil {
		return nil, fmt.Errorf("block at height %d not found", rewindHeight)
	}
	if lastValidatorSet, err = ss.LoadValidators(rewindHeight); err != nil {
		return nil, err
	}
	if validatorSet, err = ss.LoadValidators(rewindHeight + 1); err != nil {
		return nil, err
	}
	if nextValidatorSet, err = ss.LoadValidators(rewindHeight + 2); err != nil {
		return nil, err
	}

	valChangeHeight = currentState.LastHeightValidatorsChanged
	// this can only happen if the validator set changed since the last block
	if valChangeHeight > rewindHeight {
		valChangeHeight = rewindHeight + 1
	}
	paramsChangeHeight = currentState.LastHeightConsensusParamsChanged
	// this can only happen if params changed from the last block
	if paramsChangeHeight > rewindHeight {
		paramsChangeHeight = rewindHeight + 1
	}

	consensusParams, err := ss.LoadConsensusParams(rewindHeight + 1)
	if err != nil {
		return nil, err
	}

	// build the new state from the old state and the prior block
	stateRewindTo := State{
		Version: tmstate.Version{
			Consensus: tmversion.Consensus{
				Block: version.BlockProtocol,
				App:   consensusParams.Version.App,
			},
			Software: version.TMCoreSemVer,
		},
		// immutable fields
		ChainID:       currentState.ChainID,
		InitialHeight: currentState.InitialHeight,

		LastBlockHeight: rewindBlock.Header.Height,
		LastBlockID:     rewindBlock.BlockID,
		LastBlockTime:   rewindBlock.Header.Time,

		NextValidators:              nextValidatorSet,
		Validators:                  validatorSet,
		LastValidators:              lastValidatorSet,
		LastHeightValidatorsChanged: valChangeHeight,

		ConsensusParams:                  consensusParams,
		LastHeightConsensusParamsChanged: paramsChangeHeight,

		LastResultsHash: rewindBlock.Header.LastResultsHash,
		AppHash:         rewindBlock.Header.AppHash,
	}

	return &stateRewindTo, nil
}

func modifyPrivValidatorsFile(config *cfg.Config, rollbackHeight int64) {
	pval := privval.LoadOrGenFilePV(config.PrivValidatorKeyFile(), config.PrivValidatorStateFile())
	pval.LastSignState.Signature = nil
	pval.LastSignState.SignBytes = nil
	pval.LastSignState.Step = 0
	pval.LastSignState.Round = 0
	pval.LastSignState.Height = rollbackHeight
	pval.LastSignState.Save()
}

func loadStateAndBlockStore(config *cfg.Config) (*store.BlockStore, Store, error) {
	dbType := dbm.BackendType(config.DBBackend)

	if !os.FileExists(filepath.Join(config.DBDir(), "blockstore.db")) {
		return nil, nil, fmt.Errorf("no blockstore found in %v", config.DBDir())
	}

	// Get BlockStore
	blockStoreDB, err := dbm.NewDB("blockstore", dbType, config.DBDir())
	if err != nil {
		return nil, nil, err
	}
	blockStore := store.NewBlockStore(blockStoreDB)

	if !os.FileExists(filepath.Join(config.DBDir(), "state.db")) {
		return nil, nil, fmt.Errorf("no statestore found in %v", config.DBDir())
	}

	// Get StateStore
	stateDB, err := dbm.NewDB("state", dbType, config.DBDir())
	if err != nil {
		return nil, nil, err
	}
	stateStore := NewStore(stateDB, StoreOptions{
		DiscardABCIResponses: config.Storage.DiscardABCIResponses,
	})

	return blockStore, stateStore, nil
}
