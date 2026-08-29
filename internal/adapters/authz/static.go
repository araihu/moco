// Package authz implements Casbin-backed authorization adapters.
package authz

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/araihu/moco/internal/core/domain"
	"github.com/araihu/moco/internal/core/ports"
	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
)

const modelText = `[request_definition]
r = sub, dom, obj, act, secret_path

[policy_definition]
p = sub, dom, obj, act, secret_path

[role_definition]
g = _, _, _

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = (r.sub == p.sub || g(r.sub, p.sub, r.dom) || g(r.sub, p.sub, "*")) && (p.dom == "*" || r.dom == p.dom) && keyMatch3(r.obj, p.obj) && (p.act == "*" || r.act == p.act) && secretPathMatch(r.secret_path, p.secret_path)
`

const secretItemPolicyPath = "/api/v1/tenants/{tenantId}/vaults/{vaultId}/secret" //nolint:gosec // route pattern, not a credential.

// RoleBinding assigns one configured principal to a role.
type RoleBinding = ports.AuthorizationRoleBinding

// Policy grants one role or principal access to a path pattern and method.
type Policy = ports.AuthorizationPolicy

// StaticAuthorizer evaluates an in-memory policy set and supports atomic
// replacement when the authoritative persisted snapshot changes.
type StaticAuthorizer struct {
	mu       sync.RWMutex
	enforcer *casbin.SyncedEnforcer
	domains  []string
}

var _ ports.Authorizer = (*StaticAuthorizer)(nil)
var _ ports.SecretPathAuthorizer = (*StaticAuthorizer)(nil)

// NewStaticAuthorizer builds a default-deny Casbin enforcer from validated
// role bindings and allow policies.
func NewStaticAuthorizer(bindings []RoleBinding, policies []Policy) (*StaticAuthorizer, error) {
	enforcer, err := buildEnforcer(bindings, policies)
	if err != nil {
		return nil, err
	}
	return &StaticAuthorizer{enforcer: enforcer, domains: authorizationDomains(bindings, policies)}, nil
}

// Reload atomically swaps in a validated policy snapshot.
func (a *StaticAuthorizer) Reload(bindings []RoleBinding, policies []Policy) error {
	enforcer, err := buildEnforcer(bindings, policies)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.enforcer = enforcer
	a.domains = authorizationDomains(bindings, policies)
	a.mu.Unlock()
	return nil
}

func buildEnforcer(bindings []RoleBinding, policies []Policy) (*casbin.SyncedEnforcer, error) {
	parsedModel, err := model.NewModelFromString(modelText)
	if err != nil {
		return nil, fmt.Errorf("parse authorization model: %w", err)
	}
	enforcer, err := casbin.NewSyncedEnforcer(parsedModel)
	if err != nil {
		return nil, fmt.Errorf("create authorization enforcer: %w", err)
	}
	enforcer.AddFunction("secretPathMatch", secretPathMatch)
	for index, binding := range bindings {
		if !validIdentifier(binding.Principal) || !validIdentifier(binding.Role) || !validDomain(binding.Domain) {
			return nil, fmt.Errorf("role binding %d contains an invalid principal, role, or domain", index)
		}
		added, err := enforcer.AddGroupingPolicy(binding.Principal, binding.Role, binding.Domain)
		if err != nil {
			return nil, fmt.Errorf("add role binding %d: %w", index, err)
		}
		if !added {
			return nil, fmt.Errorf("role binding %d is duplicated", index)
		}
	}
	for index, policy := range policies {
		if !validIdentifier(policy.Subject) || !validDomain(policy.Domain) {
			return nil, fmt.Errorf("policy %d subject or domain is invalid", index)
		}
		if err := validatePolicyPath(policy.Path); err != nil {
			return nil, fmt.Errorf("policy %d path: %w", index, err)
		}
		if err := validateSecretPathPrefix(policy); err != nil {
			return nil, fmt.Errorf("policy %d secretPathPrefix: %w", index, err)
		}
		if !validMethod(policy.Method) {
			return nil, fmt.Errorf("policy %d method must be an uppercase HTTP method or '*': %q", index, policy.Method)
		}
		secretPathPrefix := ""
		if policy.SecretPathPrefix != nil {
			secretPathPrefix = *policy.SecretPathPrefix
		}
		added, err := enforcer.AddPolicy(policy.Subject, policy.Domain, policy.Path, policy.Method, secretPathPrefix)
		if err != nil {
			return nil, fmt.Errorf("add policy %d: %w", index, err)
		}
		if !added {
			return nil, fmt.Errorf("policy %d is duplicated", index)
		}
	}
	return enforcer, nil
}

// Authorize evaluates one request. Empty values fail closed without invoking
// the policy engine.
func (a *StaticAuthorizer) Authorize(ctx context.Context, principal, domain, resource, action string) (bool, error) {
	return a.authorize(ctx, principal, domain, resource, action, "")
}

func (a *StaticAuthorizer) authorize(ctx context.Context, principal, domain, resource, action, secretPath string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if principal == "" || domain == "" || resource == "" || action == "" {
		return false, nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	allowed, err := a.enforcer.Enforce(principal, domain, resource, action, secretPath)
	if err != nil {
		return false, fmt.Errorf("evaluate authorization policy: %w", err)
	}
	return allowed, nil
}

// AuthorizeSecretPath evaluates a secret item request including its decoded
// logical path. A policy without SecretPathPrefix uses an empty matcher and therefore
// preserves the existing vault-scoped behavior.
func (a *StaticAuthorizer) AuthorizeSecretPath(ctx context.Context, principal, domain, resource, action, secretPath string) (bool, error) {
	if secretPath == "" {
		return a.Authorize(ctx, principal, domain, resource, action)
	}
	return a.authorize(ctx, principal, domain, resource, action, secretPath)
}

// AuthorizeAnyDomain checks a resource against each configured domain. It is
// deliberately fail-closed when no domain grants the requested operation.
func (a *StaticAuthorizer) AuthorizeAnyDomain(ctx context.Context, principal, resource, action string) (bool, error) {
	a.mu.RLock()
	domains := append([]string(nil), a.domains...)
	a.mu.RUnlock()
	for _, domain := range domains {
		allowed, err := a.Authorize(ctx, principal, domain, resource, action)
		if err != nil || allowed {
			return allowed, err
		}
	}
	return false, nil
}

// AuthorizeTenant checks the canonical tenant item route for discovery
// filtering. A global policy naturally authorizes every tenant.
func (a *StaticAuthorizer) AuthorizeTenant(ctx context.Context, principal, tenantID string) (bool, error) {
	return a.Authorize(ctx, principal, tenantID, "/api/v1/tenants/"+tenantID, "GET")
}

func authorizationDomains(bindings []RoleBinding, policies []Policy) []string {
	seen := map[string]struct{}{"*": {}}
	for _, binding := range bindings {
		seen[binding.Domain] = struct{}{}
	}
	for _, policy := range policies {
		seen[policy.Domain] = struct{}{}
	}
	domains := make([]string, 0, len(seen))
	for domain := range seen {
		domains = append(domains, domain)
	}
	slices.Sort(domains)
	return domains
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
	if value == "/internal/v1/authorization" {
		return nil
	}
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

func validateSecretPathPrefix(policy Policy) error {
	if policy.SecretPathPrefix == nil {
		return nil
	}
	if policy.Path != secretItemPolicyPath {
		return fmt.Errorf("is only valid with path %q", secretItemPolicyPath)
	}
	if err := domain.ValidateSecretPrefix(policy.SecretPathPrefix); err != nil {
		return errors.New("must be a valid literal secret path prefix")
	}
	return nil
}

func secretPathMatch(args ...interface{}) (interface{}, error) {
	if len(args) != 2 {
		return false, fmt.Errorf("secretPathMatch: expected 2 arguments, got %d", len(args))
	}
	requestPath, ok := args[0].(string)
	if !ok {
		return false, errors.New("secretPathMatch: request path must be a string")
	}
	policyPath, ok := args[1].(string)
	if !ok {
		return false, errors.New("secretPathMatch: policy path must be a string")
	}
	if policyPath == "" {
		return true, nil
	}
	if requestPath == "" {
		return false, nil
	}
	return strings.HasPrefix(requestPath, policyPath), nil
}
