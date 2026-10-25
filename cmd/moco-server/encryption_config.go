package main

import "github.com/araihu/moco/internal/adapters/encryption"

func loadEncryptionKeyring(encoded string) (encryption.Keyring, error) {
	return encryption.ParseKeyring(encoded)
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
