package sequencer

import (
	"fmt"

	"github.com/morph-l2/go-ethereum/crypto"
)

// VerifyBlockSignature verifies a block's ECDSA signature against the
// expected sequencer at that block's height. All V2 blocks must carry a valid signature.
func VerifyBlockSignature(verifier SequencerVerifier, block *BlockV2) error {
	if verifier == nil {
		return fmt.Errorf("%w: verifier not configured", ErrInvalidSignature)
	}

	if len(block.Signature) == 0 {
		return fmt.Errorf("%w: missing signature at height %d", ErrInvalidSignature, block.Number)
	}

	pubKey, err := crypto.SigToPub(block.Hash.Bytes(), block.Signature)
	if err != nil {
		return fmt.Errorf("%w: recover pubkey at height %d: %v", ErrInvalidSignature, block.Number, err)
	}
	signer := crypto.PubkeyToAddress(*pubKey)

	ok, err := verifier.IsSequencerAt(signer, block.Number)
	if err != nil {
		return fmt.Errorf("IsSequencerAt height %d: %w", block.Number, err)
	}
	if !ok {
		return fmt.Errorf("%w: signer %s is not sequencer at height %d",
			ErrInvalidSignature, signer.Hex(), block.Number)
	}
	return nil
}
