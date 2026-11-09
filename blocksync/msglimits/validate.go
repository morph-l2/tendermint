package msglimits

import (
	"fmt"

	"github.com/cosmos/gogoproto/proto"
	bcproto "github.com/tendermint/tendermint/proto/tendermint/blocksync"
	"github.com/tendermint/tendermint/types"
)

const (
	MaxCommitSigs = types.MaxVotesCount
	MaxValidators = types.MaxVotesCount
	MaxBlockTxs   = 100000
	MaxEvidence   = 100000
)

func ValidateCounts(msgBytes []byte) error {
	// Pass 1 bounds the number of evidence records using a stub whose evidence
	// element is zero-size, so counting is free. This caps how many records the
	// descending pass below allocates (one struct each).
	var guard bcproto.LiteCountMessage
	_ = proto.Unmarshal(msgBytes, &guard)
	if br := guard.BlockResponse; br != nil && br.Block != nil && br.Block.Evidence != nil {
		if n := len(br.Block.Evidence.Evidence); n > MaxEvidence {
			return fmt.Errorf("too many evidence records: %d (max %d)", n, MaxEvidence)
		}
	}

	// Pass 2 descends the full tree. Every repeated leaf is the zero-size Empty
	// type, so counting is free; the only per-record allocation is the evidence
	// slice, bounded above by pass 1.
	var m bcproto.LiteMessage
	_ = proto.Unmarshal(msgBytes, &m)

	if br := m.BlockResponse; br != nil {
		if b := br.Block; b != nil {
			if d := b.Data; d != nil && len(d.Txs) > MaxBlockTxs {
				return fmt.Errorf("too many transactions: %d (max %d)", len(d.Txs), MaxBlockTxs)
			}
			// Signatures and validators accumulate across the whole message
			// (top-level commit plus every evidence light block), so the total
			// number of CommitSig / Validator structs a full decode would build
			// is bounded regardless of how they are distributed.
			sigs := 0
			if c := b.LastCommit; c != nil {
				sigs += len(c.Signatures)
				if sigs > MaxCommitSigs {
					return fmt.Errorf("too many commit signatures: %d (max %d)", sigs, MaxCommitSigs)
				}
			}
			if ev := b.Evidence; ev != nil {
				validators := 0
				for i := range ev.Evidence {
					lca := ev.Evidence[i].LightClientAttackEvidence
					if lca == nil {
						continue
					}
					validators += len(lca.ByzantineValidators)
					if cb := lca.ConflictingBlock; cb != nil {
						if sh := cb.SignedHeader; sh != nil && sh.Commit != nil {
							sigs += len(sh.Commit.Signatures)
							if sigs > MaxCommitSigs {
								return fmt.Errorf("too many commit signatures: %d (max %d)", sigs, MaxCommitSigs)
							}
						}
						if vs := cb.ValidatorSet; vs != nil {
							validators += len(vs.Validators)
						}
					}
					if validators > MaxValidators {
						return fmt.Errorf("too many validators: %d (max %d)", validators, MaxValidators)
					}
				}
			}
		}
	}
	if br := m.BlockResponseV2; br != nil {
		if b := br.Block; b != nil && len(b.Transactions) > MaxBlockTxs {
			return fmt.Errorf("too many transactions: %d (max %d)", len(b.Transactions), MaxBlockTxs)
		}
	}
	return nil
}
