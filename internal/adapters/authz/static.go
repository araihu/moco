// Package authz implements Casbin-backed authorization adapters.
package authz

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/araihu/moco/internal/core/ports"
	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
)

const modelText = `[request_definition]
r = sub, dom, obj, act

[policy_definition]
p = sub, dom, obj, act

[role_definition]
g = _, _, _

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = (r.sub == p.sub || g(r.sub, p.sub, r.dom) || g(r.sub, p.sub, "*")) && (p.dom == "*" || r.dom == p.dom) && keyMatch3(r.obj, p.obj) && (p.act == "*" || r.act == p.act)
`

// RoleBinding assigns one configured principal to a role.
type RoleBinding struct {
	Principal string `json:"principal"`
	Role      string `json:"role"`
	Domain    string `json:"domain"`
}

// Policy grants one role or principal access to a path pattern and method.
type Policy struct {
	Subject string `json:"subject"`
	Domain  string `json:"domain"`
	Path    string `json:"path"`
	Method  string `json:"method"`
}

// StaticAuthorizer evaluates an immutable in-memory policy set. Dynamic
// persistence and PolicyChangesBus reloads are intentionally a later slice.
type StaticAuthorizer struct {
	enforcer *casbin.SyncedEnforcer
}

var _ ports.Authorizer = (*StaticAuthorizer)(nil)

// NewStaticAuthorizer builds a default-deny Casbin enforcer from validated
// role bindings and allow policies.
func NewStaticAuthorizer(bindings []RoleBinding, policies []Policy) (*StaticAuthorizer, error) {
	parsedModel, err := model.NewModelFromString(modelText)
	if err != nil {
		return nil, fmt.Errorf("parse authorization model: %w", err)
	}
	enforcer, err := casbin.NewSyncedEnforcer(parsedModel)
	if err != nil {
		return nil, fmt.Errorf("create authorization enforcer: %w", err)
	}
	for index, binding := range bindings {
		if !validIdentifier(binding.Principal) || !validIdentifier(binding.Role) || !validDomain(binding.Domain) {
			return nil, fmt.Errorf("role binding %d contains an invalid principal, role, or domain", index)
		}
		if _, err := enforcer.AddGroupingPolicy(binding.Principal, binding.Role, binding.Domain); err != nil {
			return nil, fmt.Errorf("add role binding %d: %w", index, err)
		}
	}
	for index, policy := range policies {
		if !validIdentifier(policy.Subject) || !validDomain(policy.Domain) {
			return nil, fmt.Errorf("policy %d subject or domain is invalid", index)
		}
		if err := validatePolicyPath(policy.Path); err != nil {
			return nil, fmt.Errorf("policy %d path: %w", index, err)
		}
		if !validMethod(policy.Method) {
			return nil, fmt.Errorf("policy %d method must be an uppercase HTTP method or '*': %q", index, policy.Method)
		}
		if _, err := enforcer.AddPolicy(policy.Subject, policy.Domain, policy.Path, policy.Method); err != nil {
			return nil, fmt.Errorf("add policy %d: %w", index, err)
		}
	}
	return &StaticAuthorizer{enforcer: enforcer}, nil
}

// Authorize evaluates one request. Empty values fail closed without invoking
// the policy engine.
func (a *StaticAuthorizer) Authorize(ctx context.Context, principal, domain, resource, action string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if principal == "" || domain == "" || resource == "" || action == "" {
		return false, nil
	}
	allowed, err := a.enforcer.Enforce(principal, domain, resource, action)
	if err != nil {
		return false, fmt.Errorf("evaluate authorization policy: %w", err)
	}
	return allowed, nil
}

func validDomain(value string) bool {
	if value == "*" {
		return true
	}
	return validIdentifier(value)
}

func validIdentifier(value string) bool {
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

func validMethod(value string) bool {
	if value == "*" {
		return true
	}
	if len(value) < 1 || len(value) > 16 {
		return false
	}
	for _, char := range []byte(value) {
		if char < 'A' || char > 'Z' {
			return false
		}
	}
	return true
}

func validatePolicyPath(value string) error {
	if len(value) < len("/api/v1") || len(value) > 512 || (value != "/api/v1" && !strings.HasPrefix(value, "/api/v1/")) {
		return errors.New("must start with /api/v1 and contain at most 512 bytes")
	}
	if value == "/api/v1" {
		return nil
	}
	if strings.ContainsAny(value, "?#") || strings.HasSuffix(value, "/") {
		return errors.New("must not contain query/fragment delimiters or a trailing slash")
	}
	parts := strings.Split(strings.TrimPrefix(value, "/api/v1/"), "/")
	for index, part := range parts {
		if part == "" {
			return errors.New("must not contain empty path segments")
		}
		if part == "*" {
			if index != len(parts)-1 {
				return errors.New("wildcard segment must be last")
			}
			continue
		}
		if strings.HasPrefix(part, "{") || strings.HasSuffix(part, "}") {
			if len(part) < 3 || part[0] != '{' || part[len(part)-1] != '}' || !validIdentifier(part[1:len(part)-1]) {
				return errors.New("parameter segments must use {name} with a visible identifier")
			}
			continue
		}
		for _, char := range []byte(part) {
			switch {
			case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9', strings.ContainsRune("_~-", rune(char)):
			default:
				return errors.New("literal path segments contain an unsupported character")
			}
		}
	}
	return nil
}
