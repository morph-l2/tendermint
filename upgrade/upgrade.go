package upgrade

import (
	"encoding/binary"
	"fmt"
	"sync/atomic"
)

// Upgrade parameters. These are written at runtime by the consensus / blocksync paths and read
// concurrently by multiple reactors and the derivation loop, so all access goes through atomic
// load/store. The variables are unexported; callers use the accessor functions below rather than
// touching them directly, which keeps every read/write race-free.
var (
	// upgradeBlockHeight is the block height at which the consensus switches to sequencer mode.
	// Default is -1 (upgrade disabled). It is discovered at runtime once upgradeBlockTime is
	// crossed (see finalizeCommit / blocksync) and persisted via the wired store so the boundary
	// survives restarts. When < 0, the height-based upgrade check is disabled.
	upgradeBlockHeight int64 = -1

	// upgradeBlockTime is the block timestamp (Unix milliseconds) at which the consensus switches
	// to sequencer mode. Set once at node startup via SetUpgradeBlockTime. When <= 0, the
	// timestamp-based upgrade check is disabled.
	upgradeBlockTime int64

	// store, when wired via SetStore, persists upgradeBlockHeight across restarts. Set once in
	// SetStore at node startup (before the consensus/blocksync goroutines start), then only read.
	store Store

	// upgradeHeightKey is the key under which upgradeBlockHeight is persisted. It is a plain key
	// in the state DB and does not collide with state-store keys (stateKey, validatorsKey, ...).
	upgradeHeightKey = []byte("upgrade/block_height")
)

// Store is the minimal key/value subset of the node's database that the upgrade package needs.
// Keeping it tiny lets this package stay dependency-light (no tm-db import) and trivially testable
// with an in-memory map. tm-db.DB satisfies it.
type Store interface {
	Get([]byte) ([]byte, error)
	Set([]byte, []byte) error
}

// UpgradeBlockHeight returns the current upgrade block height (-1 when not yet discovered).
func UpgradeBlockHeight() int64 { return atomic.LoadInt64(&upgradeBlockHeight) }

// UpgradeBlockTime returns the configured upgrade block time in Unix milliseconds (0 = disabled).
func UpgradeBlockTime() int64 { return atomic.LoadInt64(&upgradeBlockTime) }

// SetStore wires persistence and restores any previously persisted upgrade height.
// Call once at node startup, before consensus / blocksync run.
//
// Fail-hard policy on the critical upgrade-height data:
//   - a DB read error (IO) -> return error (treating it as "nothing persisted" would silently
//     reset the boundary and let the node re-run PBFT past the upgrade after a restart);
//   - a non-empty value of unexpected length -> corrupt -> return error rather than ignore it;
//   - an absent key (len 0) is normal on first run and leaves the height at its current value.
func SetStore(s Store) error {
	store = s
	bz, err := s.Get(upgradeHeightKey)
	if err != nil {
		return fmt.Errorf("upgrade: read persisted block height: %w", err)
	}
	switch len(bz) {
	case 0:
		// absent: first run, nothing to restore.
	case 8:
		atomic.StoreInt64(&upgradeBlockHeight, int64(binary.BigEndian.Uint64(bz)))
	default:
		return fmt.Errorf("upgrade: corrupt persisted block height: got %d bytes, want 8", len(bz))
	}
	return nil
}

// IsUpgraded returns true if the given height is past the upgrade height.
// Returns false when the upgrade height is not yet set (< 0, upgrade disabled).
func IsUpgraded(height int64) bool {
	h := atomic.LoadInt64(&upgradeBlockHeight)
	if h < 0 {
		return false
	}
	return height > h
}

// IsUpgradedByTs returns true if the given block timestamp (Unix ms) is at or after the
// upgrade time. Returns false when the upgrade time is not set (<= 0, upgrade disabled).
func IsUpgradedByTs(timestamp int64) bool {
	t := atomic.LoadInt64(&upgradeBlockTime)
	if t <= 0 {
		return false
	}
	return timestamp >= t
}

// SetUpgradeBlockHeight sets the upgrade block height and, if a store is wired, persists it so the
// discovered boundary survives restarts.
//
// The height is critical consensus state: a node that loses it on restart re-runs PBFT past the
// upgrade or mishandles the boundary. If the write fails we therefore panic rather than continue
// with in-memory-only state that would silently diverge after a restart (same fail-hard policy as
// the signature store on a failed persist).
func SetUpgradeBlockHeight(height int64) {
	atomic.StoreInt64(&upgradeBlockHeight, height)
	if store != nil {
		var bz [8]byte
		binary.BigEndian.PutUint64(bz[:], uint64(height))
		if err := store.Set(upgradeHeightKey, bz[:]); err != nil {
			panic(fmt.Sprintf("upgrade: persist block height %d failed: %v", height, err))
		}
	}
}

// SetUpgradeBlockTime sets the upgrade block time (Unix milliseconds).
func SetUpgradeBlockTime(timestamp int64) {
	atomic.StoreInt64(&upgradeBlockTime, timestamp)
}
