package blssignatures

import (
	"fmt"
	"os"

	tmjson "github.com/tendermint/tendermint/libs/json"
	tmos "github.com/tendermint/tendermint/libs/os"
	"github.com/tendermint/tendermint/libs/tempfile"
)

type FileBLSKey struct {
	PubKey  []byte `json:"pub_key"`
	PrivKey []byte `json:"priv_key"`
}

func (blsKey FileBLSKey) Save(filePath string) {
	if filePath == "" {
		panic("cannot save bls key: filePath not set")
	}

	jsonBytes, err := tmjson.MarshalIndent(blsKey, "", "  ")
	if err != nil {
		panic(err)
	}

	if err := tempfile.WriteFileAtomic(filePath, jsonBytes, 0600); err != nil {
		panic(err)
	}
}

func GenFileBLSKey() *FileBLSKey {
	pubKey, privKey, err := GenerateKeys()
	if err != nil {
		panic(err)
	}

	return &FileBLSKey{
		PubKey:  PublicKeyToBytes(pubKey),
		PrivKey: PrivateKeyToBytes(privKey),
	}
}

func LoadBLSKey(filePath string) *FileBLSKey {
	keyJSONBytes, err := os.ReadFile(filePath)
	if err != nil {
		tmos.Exit(err.Error())
	}
	blsKey := FileBLSKey{}
	if err := tmjson.Unmarshal(keyJSONBytes, &blsKey); err != nil {
		tmos.Exit(fmt.Sprintf("Error reading PrivValidator key from %v: %v\n", filePath, err))
	}

	return &blsKey
}
