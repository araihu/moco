package main

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

type encryptionKeyring struct {
	ActiveKeyID string
	Keys        map[string][]byte
}

func loadEncryptionKeyring(encoded string) (encryptionKeyring, error) {
	if len(encoded) > maxEncryptionKeyringBytes {
		return encryptionKeyring{}, errors.New("MOCO_ENCRYPTION_KEYS exceeds 64 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(encoded)))
	decoder.DisallowUnknownFields()
	var document encryptionKeyringDocument
	if err := decoder.Decode(&document); err != nil {
		return encryptionKeyring{}, fmt.Errorf("decode MOCO_ENCRYPTION_KEYS: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return encryptionKeyring{}, errors.New("MOCO_ENCRYPTION_KEYS must contain exactly one JSON value")
	}
	if !validEncryptionKeyID(document.ActiveKeyID) {
		return encryptionKeyring{}, errors.New("MOCO_ENCRYPTION_KEYS activeKeyId is invalid")
	}
	if len(document.Keys) == 0 || len(document.Keys) > maxEncryptionKeys {
		return encryptionKeyring{}, fmt.Errorf("MOCO_ENCRYPTION_KEYS keys must contain between 1 and %d entries", maxEncryptionKeys)
	}
	keys := make(map[string][]byte, len(document.Keys))
	for keyID, encodedKey := range document.Keys {
		if !validEncryptionKeyID(keyID) {
			wipeConfigurationKeys(keys)
			return encryptionKeyring{}, errors.New("MOCO_ENCRYPTION_KEYS contains an invalid key ID")
		}
		key, err := base64.StdEncoding.Strict().DecodeString(encodedKey)
		if err != nil || len(key) != 32 {
			wipeConfigurationKey(key)
			wipeConfigurationKeys(keys)
			return encryptionKeyring{}, errors.New("MOCO_ENCRYPTION_KEYS entries must be standard base64 encoding of exactly 32 bytes")
		}
		keys[keyID] = key
	}
	if _, ok := keys[document.ActiveKeyID]; !ok {
		wipeConfigurationKeys(keys)
		return encryptionKeyring{}, errors.New("MOCO_ENCRYPTION_KEYS activeKeyId is missing from keys")
	}
	return encryptionKeyring{ActiveKeyID: document.ActiveKeyID, Keys: keys}, nil
}

func validEncryptionKeyID(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, char := range []byte(value) {
		if char < 0x21 || char > 0x7e {
			return false
		}
	}
	return true
}
