package domain

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// MaxSecretPathLength is the maximum logical path or prefix length.
	MaxSecretPathLength = 1024
	// MaxSecretValueBytes is the decoded secret value limit.
	MaxSecretValueBytes = 1 << 20
	// MaxSecretContentTypeLength is the maximum media type length.
	MaxSecretContentTypeLength = 255
	// DefaultSecretContentType applies when a write omits contentType.
	DefaultSecretContentType = "application/octet-stream"
)

var ErrSecretTooLarge = errors.New("decoded secret value exceeds 1 MiB")

// SecretMetadata is safe for metadata-only reads and lists. Paths can still be
// sensitive and must not be logged by default.
type SecretMetadata struct {
	Sequence    int64
	TenantID    string
	VaultID     string
	Path        string
	Version     int64
	Digest      string
	ContentType string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Secret is one decrypted value plus its lifecycle metadata.
type Secret struct {
	Metadata SecretMetadata
	Value    []byte
}

// SecretWrite contains caller-controlled secret material.
type SecretWrite struct {
	Value       []byte
	ContentType *string
}

// ValidateSecretWrite validates a decoded secret write and returns its
// normalized content type.
func ValidateSecretWrite(input SecretWrite) (string, error) {
	if len(input.Value) > MaxSecretValueBytes {
		return "", ErrSecretTooLarge
	}
	var violations []FieldViolation
	if len(input.Value) == 0 {
		violations = append(violations, FieldViolation{
			Field: "/value", Code: "invalid_length", Message: "must contain between 1 byte and 1 MiB",
		})
	}
	contentType := DefaultSecretContentType
	if input.ContentType != nil {
		contentType = *input.ContentType
		if length := utf8.RuneCountInString(contentType); !utf8.ValidString(contentType) || length < 1 || length > MaxSecretContentTypeLength {
			violations = append(violations, FieldViolation{
				Field: "/contentType", Code: "invalid_length", Message: "must contain between 1 and 255 characters",
			})
		}
	}
	return contentType, validationResult(violations)
}

// ValidateSecretPath validates one complete logical secret path.
func ValidateSecretPath(path string) error {
	return validateSecretLocation("path", path, false)
}

// ValidateSecretPrefix validates an optional S3-like literal prefix.
func ValidateSecretPrefix(prefix *string) error {
	if prefix == nil {
		return nil
	}
	return validateSecretLocation("prefix", *prefix, true)
}

func validateSecretLocation(field, value string, trailingSlash bool) error {
	var violations []FieldViolation
	length := utf8.RuneCountInString(value)
	if !utf8.ValidString(value) || length < 1 || length > MaxSecretPathLength {
		violations = append(violations, FieldViolation{
			Field: field, Code: "invalid_length", Message: "must contain between 1 and 1024 characters",
		})
		return validationResult(violations)
	}
	trimmed := value
	if trailingSlash {
		trimmed = strings.TrimSuffix(trimmed, "/")
	}
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == "" || segment == "." || segment == ".." {
			violations = append(violations, FieldViolation{
				Field: field, Code: "invalid_format", Message: "must not contain empty, '.', or '..' segments",
			})
			break
		}
	}
	return validationResult(violations)
}
