package encryption

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	maxEncryptionKeyringBytes = 64 << 10
	maxEncryptionKeys         = 16
)

type encryptionKeyringDocument struct {
	ActiveKeyID string            `json:"activeKeyId"`
	Keys        map[string]string `json:"keys"`
}

// Keyring contains decoded deployment root keys. Destroy it after use.
type Keyring struct {
	ActiveKeyID string
	Keys        map[string][]byte
}

// ParseKeyring decodes the same strict JSON document used by MOCO_ENCRYPTION_KEYS.
func ParseKeyring(encoded string) (Keyring, error) {
	if len(encoded) > maxEncryptionKeyringBytes {
		return Keyring{}, errors.New("MOCO_ENCRYPTION_KEYS exceeds 64 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(encoded)))
	decoder.DisallowUnknownFields()
	var document encryptionKeyringDocument
	if err := decoder.Decode(&document); err != nil {
		return Keyring{}, fmt.Errorf("decode MOCO_ENCRYPTION_KEYS: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Keyring{}, errors.New("MOCO_ENCRYPTION_KEYS must contain exactly one JSON value")
	}
	if !validKeyID(document.ActiveKeyID) {
		return Keyring{}, errors.New("MOCO_ENCRYPTION_KEYS activeKeyId is invalid")
	}
	if len(document.Keys) == 0 || len(document.Keys) > maxEncryptionKeys {
		return Keyring{}, fmt.Errorf("MOCO_ENCRYPTION_KEYS keys must contain between 1 and %d entries", maxEncryptionKeys)
	}
	keys := make(map[string][]byte, len(document.Keys))
	for keyID, encodedKey := range document.Keys {
		if !validKeyID(keyID) {
			wipeKeyring(keys)
			return Keyring{}, errors.New("MOCO_ENCRYPTION_KEYS contains an invalid key ID")
		}
		key, err := base64.StdEncoding.Strict().DecodeString(encodedKey)
		if err != nil || len(key) != 32 {
			wipe(key)
			wipeKeyring(keys)
			return Keyring{}, errors.New("MOCO_ENCRYPTION_KEYS entries must be standard base64 encoding of exactly 32 bytes")
		}
		keys[keyID] = key
	}
	if _, ok := keys[document.ActiveKeyID]; !ok {
		wipeKeyring(keys)
		return Keyring{}, errors.New("MOCO_ENCRYPTION_KEYS activeKeyId is missing from keys")
	}
	return Keyring{ActiveKeyID: document.ActiveKeyID, Keys: keys}, nil
}

// Destroy clears decoded root-key material owned by this keyring.
func (k *Keyring) Destroy() {
	wipeKeyring(k.Keys)
}
