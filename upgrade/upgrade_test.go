package upgrade

import (
	"errors"
	"testing"
)

// memStore is an in-memory Store for tests.
type memStore map[string][]byte

func (m memStore) Get(k []byte) ([]byte, error) { return m[string(k)], nil }
func (m memStore) Set(k, v []byte) error        { m[string(k)] = v; return nil }

// errStore is a Store whose Get/Set can be made to fail, to exercise the
// fail-hard policy on the critical upgrade-height data.
type errStore struct {
	data   map[string][]byte
	getErr error
	setErr error
}

func (s *errStore) Get(k []byte) ([]byte, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.data[string(k)], nil
}

func (s *errStore) Set(k, v []byte) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.data[string(k)] = v
	return nil
}

// reset restores package globals so tests do not leak state into each other.
func reset() {
	upgradeBlockHeight = -1
	upgradeBlockTime = 0
	store = nil
}

func TestIsUpgradedByTs(t *testing.T) {
	reset()
	defer reset()

	// Disabled when unset.
	if IsUpgradedByTs(1) {
		t.Fatal("expected disabled when upgrade time <= 0")
	}

	SetUpgradeBlockTime(1000)
	cases := []struct {
		ts   int64
		want bool
	}{
		{999, false}, // before
		{1000, true}, // exactly at boundary (>=)
		{1001, true}, // after
	}
	for _, c := range cases {
		if got := IsUpgradedByTs(c.ts); got != c.want {
			t.Fatalf("IsUpgradedByTs(%d) = %v, want %v", c.ts, got, c.want)
		}
	}
}

func TestSetUpgradeBlockHeightPersists(t *testing.T) {
	reset()
	defer reset()

	db := memStore{}

	// Fresh store, nothing persisted: load leaves the -1 sentinel.
	if err := SetStore(db); err != nil {
		t.Fatalf("SetStore: %v", err)
	}
	if UpgradeBlockHeight() != -1 {
		t.Fatalf("expected -1 after empty load, got %d", UpgradeBlockHeight())
	}

	// Discovering the boundary persists it.
	SetUpgradeBlockHeight(42)
	if UpgradeBlockHeight() != 42 {
		t.Fatalf("expected 42, got %d", UpgradeBlockHeight())
	}

	// Simulate a restart: clear globals, re-wire the same DB, expect the height restored.
	upgradeBlockHeight = -1
	store = nil
	if err := SetStore(db); err != nil {
		t.Fatalf("SetStore: %v", err)
	}
	if UpgradeBlockHeight() != 42 {
		t.Fatalf("expected 42 restored after restart, got %d", UpgradeBlockHeight())
	}
	if !IsUpgraded(43) || IsUpgraded(42) {
		t.Fatalf("IsUpgraded boundary wrong after restore: 42=%v 43=%v", IsUpgraded(42), IsUpgraded(43))
	}
}

func TestSetUpgradeBlockHeightNoStore(t *testing.T) {
	reset()
	defer reset()

	// No store wired: must not panic, just sets the global.
	SetUpgradeBlockHeight(7)
	if UpgradeBlockHeight() != 7 {
		t.Fatalf("expected 7, got %d", UpgradeBlockHeight())
	}
}

// A DB read failure must propagate (not be swallowed as "nothing persisted"),
// otherwise a restart would silently reset the boundary.
func TestSetStoreReadErrorPropagates(t *testing.T) {
	reset()
	defer reset()

	s := &errStore{getErr: errors.New("db read failed")}
	if err := SetStore(s); err == nil {
		t.Fatal("expected error when DB read fails")
	}
	if UpgradeBlockHeight() != -1 {
		t.Fatalf("expected -1 unchanged on read error, got %d", UpgradeBlockHeight())
	}
}

// A persisted value of unexpected (non-zero, non-8) length is corruption and must error,
// not be silently ignored (which would leave the boundary at the wrong value).
func TestSetStoreCorruptLengthErrors(t *testing.T) {
	reset()
	defer reset()

	db := memStore{}
	db[string(upgradeHeightKey)] = []byte{0x1, 0x2, 0x3, 0x4} // 4 bytes -> corrupt
	if err := SetStore(db); err == nil {
		t.Fatal("expected error on corrupt (non-8-byte) persisted height")
	}
	if UpgradeBlockHeight() != -1 {
		t.Fatalf("expected -1 unchanged on corrupt value, got %d", UpgradeBlockHeight())
	}
}

// A failed persist of the critical upgrade height must stop the node (panic),
// not continue with in-memory-only state that diverges after a restart.
func TestSetUpgradeBlockHeightPanicsOnWriteFailure(t *testing.T) {
	reset()
	defer reset()

	s := &errStore{data: map[string][]byte{}, setErr: errors.New("disk full")}
	if err := SetStore(s); err != nil {
		t.Fatalf("SetStore: %v", err)
	}

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when persisting block height fails")
		}
	}()
	SetUpgradeBlockHeight(99)
}
