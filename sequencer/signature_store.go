package sequencer

import (
	"fmt"

	"github.com/morph-l2/go-ethereum/common"
	dbm "github.com/tendermint/tm-db"
)

var signaturePrefix = []byte("block_sig:")

// SignatureStore persists block signatures independently from geth.
// Key = "block_sig:" + blockHash (32 bytes), Value = signature (65 bytes).
type SignatureStore struct {
	db dbm.DB
}

// NewSignatureStore creates a new SignatureStore backed by the given DB.
func NewSignatureStore(db dbm.DB) *SignatureStore {
	return &SignatureStore{db: db}
}

func signatureKey(blockHash common.Hash) []byte {
	key := make([]byte, len(signaturePrefix)+common.HashLength)
	copy(key, signaturePrefix)
	copy(key[len(signaturePrefix):], blockHash.Bytes())
	return key
}

// SaveSignature persists a block's signature.
func (s *SignatureStore) SaveSignature(blockHash common.Hash, sig []byte) error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Set(signatureKey(blockHash), sig)
}

// GetSignature retrieves a block's signature. Returns nil, error if not found.
func (s *SignatureStore) GetSignature(blockHash common.Hash) ([]byte, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("signature store not initialized")
	}
	val, err := s.db.Get(signatureKey(blockHash))
	if err != nil {
		return nil, err
	}
	if len(val) == 0 {
		return nil, fmt.Errorf("signature not found for block %s", blockHash.Hex())
	}
	return val, nil
}
