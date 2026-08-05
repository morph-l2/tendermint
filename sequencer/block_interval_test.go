package sequencer

import (
	"testing"
	"time"
)

// TestSetBlockIntervals covers the block-production interval overrides used by
// the sequencer. The intervals are package-level state read by NewStateV2, so
// each subtest saves and restores them to avoid cross-test contamination.
func TestSetBlockIntervals(t *testing.T) {
	origBlock, origFast := BlockInterval, FastBlockInterval
	t.Cleanup(func() { BlockInterval, FastBlockInterval = origBlock, origFast })

	t.Run("valid pair is applied", func(t *testing.T) {
		BlockInterval, FastBlockInterval = origBlock, origFast
		if err := SetBlockIntervals(2*time.Second, 300*time.Millisecond); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if BlockInterval != 2*time.Second {
			t.Errorf("BlockInterval = %s, want 2s", BlockInterval)
		}
		if FastBlockInterval != 300*time.Millisecond {
			t.Errorf("FastBlockInterval = %s, want 300ms", FastBlockInterval)
		}
	})

	t.Run("fast equal to block is rejected", func(t *testing.T) {
		if err := SetBlockIntervals(2*time.Second, 2*time.Second); err == nil {
			t.Fatal("expected error when fast == block, got nil")
		}
	})

	t.Run("fast greater than block is rejected", func(t *testing.T) {
		if err := SetBlockIntervals(2*time.Second, 3*time.Second); err == nil {
			t.Fatal("expected error when fast > block, got nil")
		}
	})

	t.Run("non-positive values are rejected", func(t *testing.T) {
		if err := SetBlockIntervals(0, 300*time.Millisecond); err == nil {
			t.Fatal("expected error when block <= 0, got nil")
		}
		if err := SetBlockIntervals(2*time.Second, 0); err == nil {
			t.Fatal("expected error when fast <= 0, got nil")
		}
	})

	t.Run("rejected values leave previous state intact", func(t *testing.T) {
		if err := SetBlockIntervals(2*time.Second, 300*time.Millisecond); err != nil {
			t.Fatalf("setup failed: %v", err)
		}
		if err := SetBlockIntervals(2*time.Second, 3*time.Second); err == nil {
			t.Fatal("expected error, got nil")
		}
		if BlockInterval != 2*time.Second || FastBlockInterval != 300*time.Millisecond {
			t.Errorf("intervals mutated on error: block=%s fast=%s", BlockInterval, FastBlockInterval)
		}
	})
}
