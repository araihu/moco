package main

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestLoadConfigurationDecodesEncryptionKey(t *testing.T) {
	rootKey := bytes.Repeat([]byte{0x5a}, 32)
	t.Setenv("MOCO_BEARER_TOKEN", "test-bearer-token-with-at-least-32-bytes")
	t.Setenv("MOCO_CURSOR_HMAC_KEY", "test-cursor-key-with-at-least-32-bytes")
	t.Setenv("MOCO_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(rootKey))
	t.Setenv("MOCO_ENCRYPTION_KEY_ID", "")

	configuration, err := loadConfiguration()
	if err != nil {
		t.Fatalf("load configuration: %v", err)
	}
	if configuration.encryptionKeyID != "local-v1" || !bytes.Equal(configuration.encryptionKey, rootKey) {
		t.Fatalf("unexpected encryption configuration: ID %q, %d key bytes", configuration.encryptionKeyID, len(configuration.encryptionKey))
	}
}

func TestLoadConfigurationRejectsMalformedEncryptionKey(t *testing.T) {
	t.Setenv("MOCO_BEARER_TOKEN", "test-bearer-token-with-at-least-32-bytes")
	t.Setenv("MOCO_CURSOR_HMAC_KEY", "test-cursor-key-with-at-least-32-bytes")

	for name, value := range map[string]string{
		"missing":    "",
		"not-base64": "not base64",
		"short":      base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x01}, 31)),
		"raw":        base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{0x01}, 32)),
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("MOCO_ENCRYPTION_KEY", value)
			if _, err := loadConfiguration(); err == nil {
				t.Fatal("malformed encryption key unexpectedly accepted")
			}
		})
	}
}
