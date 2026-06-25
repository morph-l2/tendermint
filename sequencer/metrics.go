package sequencer

import (
	"time"

	"github.com/go-kit/kit/metrics"
)

const (
	// MetricsSubsystem is the subsystem label shared by all metrics exposed by
	// the sequencer package (block production, P2P broadcast and blocksync V2).
	// With the "morphnode" namespace this yields names like
	// morphnode_sequencer_*.
	MetricsSubsystem = "sequencer"
)

//go:generate go run ../scripts/metricsgen -struct=Metrics

// Metrics contains the metrics exposed by the centralized sequencer stack that
// replaces PBFT consensus after the upgrade: block production (StateV2), the
// block broadcast reactor and the blocksync V2 catch-up path. A single struct
// is shared by all three so they land under one subsystem.
//
// All values are integer-valued. Short processing latencies use integer
// milliseconds (sub-second work would round to 0 in seconds); the block
// interval and L1 lag are large-scale and use integer seconds. Use the typed
// helper methods below so call sites never deal with float64 directly.
type Metrics struct {
	// ---- Block production (StateV2) ----

	// Whether this node is currently the active sequencer (1) or not (0).
	IsActiveSequencer metrics.Gauge

	// Total number of blocks produced by this node while acting as the active
	// sequencer. Only advances on the leader/active node.
	BlocksProducedTotal metrics.Counter

	// Whole seconds between consecutive applied blocks (block-timestamp delta).
	BlockIntervalSeconds metrics.Histogram `metrics_buckettype:"exp" metrics_bucketsizes:"1, 2, 6"`

	// Size in bytes of applied V2 blocks.
	BlockSizeBytes metrics.Histogram `metrics_buckettype:"exp" metrics_bucketsizes:"1024, 2, 16"`

	// Milliseconds per block-apply step, labeled by step (sig store / geth).
	ApplyDurationMilliseconds metrics.Histogram `metrics_labels:"step" metrics_buckettype:"exp" metrics_bucketsizes:"1, 2, 14"`

	// Number of transactions per applied block.
	BlockTxs metrics.Histogram `metrics_buckettype:"exp" metrics_bucketsizes:"1, 2, 14"`

	// Total blocks dropped because the local broadcast channel was full.
	BroadcastChannelDroppedTotal metrics.Counter

	// Milliseconds to assemble a block from the L2 node.
	AssembleDurationMilliseconds metrics.Histogram `metrics_buckettype:"exp" metrics_bucketsizes:"1, 2, 14"`

	// Milliseconds to sign a produced block.
	SignDurationMilliseconds metrics.Histogram `metrics_buckettype:"exp" metrics_bucketsizes:"1, 2, 14"`

	// Milliseconds to commit a produced block, labeled by mode (single-node).
	CommitDurationMilliseconds metrics.Histogram `metrics_labels:"mode" metrics_buckettype:"exp" metrics_bucketsizes:"1, 2, 14"`

	// Current depth of the local broadcast channel.
	BroadcastChannelDepth metrics.Gauge

	// ---- Block broadcast reactor ----

	// Number of blocks this node is behind the broadcast tip.
	BcastSyncGap metrics.Gauge

	// Total V2 blocks applied via the broadcast reactor, labeled by source
	// (p2p / cache).
	BcastBlocksAppliedTotal metrics.Counter `metrics_labels:"source"`

	// Whether the broadcast reactor routines are running (1) or stopped (0).
	BcastRoutinesStarted metrics.Gauge

	// Total peers banned by the broadcast reactor, labeled by reason. An
	// invalid-signature (forged) block bans its sender and is surfaced here.
	BcastPeersBannedTotal metrics.Counter `metrics_labels:"reason"`

	// Number of blocks held in the pending (out-of-order) cache.
	BcastPendingCacheSize metrics.Gauge

	// Total inbound blocks dropped as already-seen duplicates.
	BcastBlocksDedupedTotal metrics.Counter

	// ---- Blocksync V2 catch-up ----

	// Total V2 blocks applied via the blocksync catch-up path. The apply rate
	// is derived on the query side with rate(...) — no float gauge is kept.
	SyncV2BlocksTotal metrics.Counter
}

// ---- Typed helpers (keep float64 conversions out of call sites) ----

// setBool sets a 0/1 gauge from a bool.
func setBool(g metrics.Gauge, b bool) {
	if b {
		g.Set(1)
	} else {
		g.Set(0)
	}
}

// SetActiveSequencer records whether this node is the active sequencer.
func (m *Metrics) SetActiveSequencer(active bool) { setBool(m.IsActiveSequencer, active) }

// IncBlocksProduced counts one block produced by this node.
func (m *Metrics) IncBlocksProduced() { m.BlocksProducedTotal.Add(1) }

// ObserveBlockIntervalSeconds records the whole-second gap between blocks.
func (m *Metrics) ObserveBlockIntervalSeconds(secs uint64) {
	m.BlockIntervalSeconds.Observe(float64(secs))
}

// ObserveBlockSizeBytes records an applied block's wire size in bytes.
func (m *Metrics) ObserveBlockSizeBytes(n int) { m.BlockSizeBytes.Observe(float64(n)) }

// ObserveApplyDuration records a block-apply step latency in milliseconds.
func (m *Metrics) ObserveApplyDuration(step string, d time.Duration) {
	m.ApplyDurationMilliseconds.With("step", step).Observe(float64(d.Milliseconds()))
}

// ObserveBlockTxs records the transaction count of an applied block.
func (m *Metrics) ObserveBlockTxs(n int) { m.BlockTxs.Observe(float64(n)) }

// IncBroadcastChannelDropped counts one block dropped (broadcast channel full).
func (m *Metrics) IncBroadcastChannelDropped() { m.BroadcastChannelDroppedTotal.Add(1) }

// ObserveAssembleDuration records block-assemble latency in milliseconds.
func (m *Metrics) ObserveAssembleDuration(d time.Duration) {
	m.AssembleDurationMilliseconds.Observe(float64(d.Milliseconds()))
}

// ObserveSignDuration records block-sign latency in milliseconds.
func (m *Metrics) ObserveSignDuration(d time.Duration) {
	m.SignDurationMilliseconds.Observe(float64(d.Milliseconds()))
}

// ObserveCommitDuration records single-node commit latency in milliseconds.
func (m *Metrics) ObserveCommitDuration(mode string, d time.Duration) {
	m.CommitDurationMilliseconds.With("mode", mode).Observe(float64(d.Milliseconds()))
}

// SetBroadcastChannelDepth records the current broadcast channel depth.
func (m *Metrics) SetBroadcastChannelDepth(n int) { m.BroadcastChannelDepth.Set(float64(n)) }

// SetBcastSyncGap records how many blocks this node is behind the tip.
func (m *Metrics) SetBcastSyncGap(gap int64) { m.BcastSyncGap.Set(float64(gap)) }

// IncBcastBlocksApplied counts one block applied via the broadcast reactor.
func (m *Metrics) IncBcastBlocksApplied(source string) {
	m.BcastBlocksAppliedTotal.With("source", source).Add(1)
}

// SetBcastRoutinesStarted records whether the reactor routines are running.
func (m *Metrics) SetBcastRoutinesStarted(started bool) { setBool(m.BcastRoutinesStarted, started) }

// IncBcastPeersBanned counts one peer ban with a low-cardinality reason token.
func (m *Metrics) IncBcastPeersBanned(reason string) {
	m.BcastPeersBannedTotal.With("reason", reason).Add(1)
}

// SetBcastPendingCacheSize records the pending (out-of-order) cache size.
func (m *Metrics) SetBcastPendingCacheSize(n int) { m.BcastPendingCacheSize.Set(float64(n)) }

// IncBcastBlocksDeduped counts one inbound duplicate block dropped.
func (m *Metrics) IncBcastBlocksDeduped() { m.BcastBlocksDedupedTotal.Add(1) }

// IncSyncV2Blocks counts one block applied via blocksync catch-up.
func (m *Metrics) IncSyncV2Blocks() { m.SyncV2BlocksTotal.Add(1) }
