package ports

import (
	"context"
	"errors"
)

// Authorizer evaluates one authenticated principal against an API resource.
// Implementations must fail closed when no policy matches.
type Authorizer interface {
	Authorize(context.Context, string, string, string, string) (bool, error)
}

// AnyDomainAuthorizer checks whether a principal has a permission in at least
// one configured authorization domain. It is used only for collection routes
// whose URL does not contain a tenant ID yet.
type AnyDomainAuthorizer interface {
	AuthorizeAnyDomain(context.Context, string, string, string) (bool, error)
}

// TenantVisibilityAuthorizer evaluates the tenant item permission used to
// filter tenant discovery for principals with tenant-scoped policies.
type TenantVisibilityAuthorizer interface {
	AuthorizeTenant(context.Context, string, string) (bool, error)
}

// SecretPathAuthorizer evaluates a secret item request against its logical
// path. Implementations must retain the route-level resource and action
// checks, while allowing a policy to narrow access to a literal path prefix.
type SecretPathAuthorizer interface {
	AuthorizeSecretPath(context.Context, string, string, string, string, string) (bool, error)
}

// SecretPrefixAuthorizer evaluates a secret collection request against its
// decoded logical prefix. An empty prefix represents an unscoped collection
// request and must not satisfy a path-scoped policy.
type SecretPrefixAuthorizer interface {
	AuthorizeSecretPrefix(context.Context, string, string, string, string, string) (bool, error)
}

// AuthorizationPrincipal identifies a bearer credential by its token digest.
type AuthorizationPrincipal struct {
	PrincipalID string `json:"id"`
	TokenSHA256 string `json:"tokenSha256"`
}

// AuthorizationRoleBinding assigns one principal to a role in a domain.
type AuthorizationRoleBinding struct {
	Principal string `json:"principal"`
	Role      string `json:"role"`
	Domain    string `json:"domain"`
}

// AuthorizationPolicy grants one role or principal access to a resource.
type AuthorizationPolicy struct {
	Subject          string  `json:"subject"`
	Domain           string  `json:"domain"`
	Path             string  `json:"path"`
	Method           string  `json:"method"`
	SecretPathPrefix *string `json:"secretPathPrefix,omitempty"`
}

// AuthorizationState is the authoritative persisted policy snapshot.
type AuthorizationState struct {
	Initialized bool
	// Revision is the monotonic snapshot revision. Loaders use it to detect
	// changes made by another writer, while writers supply the revision they
	// observed as an optimistic-concurrency precondition.
	Revision     int64
	RoleBindings []AuthorizationRoleBinding
	Policies     []AuthorizationPolicy
}

// ErrAuthorizationRevisionConflict means that a writer attempted to replace
// a snapshot based on a stale revision. The persisted state is unchanged.
var ErrAuthorizationRevisionConflict = errors.New("authorization snapshot revision conflict")

// AuthorizationRepository stores and loads the authoritative policy snapshot.
// ReplaceAuthorization must atomically replace the complete snapshot only when
// state.Revision is still current, increment that revision, and commit before
// returning. A stale revision returns ErrAuthorizationRevisionConflict.
type AuthorizationRepository interface {
	LoadAuthorization(context.Context) (AuthorizationState, error)
	ReplaceAuthorization(context.Context, AuthorizationState) error
}
