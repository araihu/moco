package domain

import (
	"fmt"
	"regexp"
	"time"
	"unicode/utf8"
)

const (
	MaxTenantNameLength        = 128
	MaxTenantDescriptionLength = 1024
	MaxTenantExternalIDLength  = 512
	MaxTenantLabels            = 64
	MaxTenantLabelValueLength  = 256
	MinIdempotencyKeyLength    = 8
	MaxIdempotencyKeyLength    = 128
)

var tenantNamePattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$`)

// Tenant is the transport- and persistence-independent tenant aggregate.
type Tenant struct {
	Sequence    int64             `json:"-"`
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description *string           `json:"description,omitempty"`
	ExternalID  *string           `json:"externalId,omitempty"`
	Labels      map[string]string `json:"labels"`
	Revision    int64             `json:"revision"`
	CreatedAt   time.Time         `json:"createdAt"`
	UpdatedAt   time.Time         `json:"updatedAt"`
}

// TenantCreate contains caller-controlled fields for tenant creation.
type TenantCreate struct {
	Name        string
	Description *string
	ExternalID  *string
	Labels      map[string]string
}

// TenantUpdate contains the complete mutable tenant state.
type TenantUpdate struct {
	Name        string
	Description *string
	Labels      map[string]string
}

// FieldViolation is a stable, safe-to-return validation failure.
type FieldViolation struct {
	Field   string
	Code    string
	Message string
}

// ValidationError reports all validation failures found in one input.
type ValidationError struct {
	Violations []FieldViolation
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation failed for %d field(s)", len(e.Violations))
}

// ValidateTenantCreate validates a creation request against the public contract.
func ValidateTenantCreate(input TenantCreate) error {
	violations := validateTenantFields(input.Name, input.Description, input.Labels)
	if input.ExternalID != nil {
		length := utf8.RuneCountInString(*input.ExternalID)
		if length < 1 || length > MaxTenantExternalIDLength {
			violations = append(violations, FieldViolation{
				Field:   "/externalId",
				Code:    "invalid_length",
				Message: "must contain between 1 and 512 characters",
			})
		}
	}
	return validationResult(violations)
}

// ValidateTenantUpdate validates a full replacement of mutable tenant fields.
func ValidateTenantUpdate(input TenantUpdate) error {
	return validationResult(validateTenantFields(input.Name, input.Description, input.Labels))
}

// ValidateTenantFilters validates exact list filters.
func ValidateTenantFilters(name, externalID *string) error {
	var violations []FieldViolation
	if name != nil {
		length := utf8.RuneCountInString(*name)
		if length < 1 || length > MaxTenantNameLength {
			violations = append(violations, FieldViolation{
				Field:   "name",
				Code:    "invalid_length",
				Message: "must contain between 1 and 128 characters",
			})
		}
	}
	if externalID != nil {
		length := utf8.RuneCountInString(*externalID)
		if length < 1 || length > MaxTenantExternalIDLength {
			violations = append(violations, FieldViolation{
				Field:   "externalId",
				Code:    "invalid_length",
				Message: "must contain between 1 and 512 characters",
			})
		}
	}
	return validationResult(violations)
}

// ValidateIdempotencyKey validates an optional creation idempotency key.
func ValidateIdempotencyKey(key string) error {
	var violations []FieldViolation
	if len(key) < MinIdempotencyKeyLength || len(key) > MaxIdempotencyKeyLength {
		violations = append(violations, FieldViolation{
			Field:   "Idempotency-Key",
			Code:    "invalid_length",
			Message: "must contain between 8 and 128 visible ASCII characters",
		})
	}
	for _, char := range []byte(key) {
		if char < 0x21 || char > 0x7e {
			violations = append(violations, FieldViolation{
				Field:   "Idempotency-Key",
				Code:    "invalid_format",
				Message: "must contain only visible ASCII characters",
			})
			break
		}
	}
	return validationResult(violations)
}

// CloneLabels returns a non-nil defensive copy suitable for an API response.
func CloneLabels(labels map[string]string) map[string]string {
	cloned := make(map[string]string, len(labels))
	for key, value := range labels {
		cloned[key] = value
	}
	return cloned
}

func validateTenantFields(name string, description *string, labels map[string]string) []FieldViolation {
	var violations []FieldViolation
	if length := utf8.RuneCountInString(name); length < 1 || length > MaxTenantNameLength {
		violations = append(violations, FieldViolation{
			Field:   "/name",
			Code:    "invalid_length",
			Message: "must contain between 1 and 128 characters",
		})
	} else if !tenantNamePattern.MatchString(name) {
		violations = append(violations, FieldViolation{
			Field:   "/name",
			Code:    "invalid_format",
			Message: "must match the documented tenant name format",
		})
	}
	if description != nil && utf8.RuneCountInString(*description) > MaxTenantDescriptionLength {
		violations = append(violations, FieldViolation{
			Field:   "/description",
			Code:    "invalid_length",
			Message: "must contain at most 1024 characters",
		})
	}
	if len(labels) > MaxTenantLabels {
		violations = append(violations, FieldViolation{
			Field:   "/labels",
			Code:    "too_many_properties",
			Message: "must contain at most 64 properties",
		})
	}
	for _, value := range labels {
		if utf8.RuneCountInString(value) > MaxTenantLabelValueLength {
			violations = append(violations, FieldViolation{
				Field:   "/labels",
				Code:    "invalid_value_length",
				Message: "label values must contain at most 256 characters",
			})
			break
		}
	}
	return violations
}

func validationResult(violations []FieldViolation) error {
	if len(violations) == 0 {
		return nil
	}
	return &ValidationError{Violations: violations}
}
