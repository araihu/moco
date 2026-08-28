package ports

import "context"

// Authorizer evaluates one authenticated principal against an API resource.
// Implementations must fail closed when no policy matches.
type Authorizer interface {
	Authorize(context.Context, string, string, string, string) (bool, error)
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
	Subject string `json:"subject"`
	Domain  string `json:"domain"`
	Path    string `json:"path"`
	Method  string `json:"method"`
}

// AuthorizationState is the authoritative persisted policy snapshot.
type AuthorizationState struct {
	Initialized  bool
	RoleBindings []AuthorizationRoleBinding
	Policies     []AuthorizationPolicy
}

// AuthorizationRepository stores and loads the authoritative policy snapshot.
// ReplaceAuthorization must commit the complete snapshot before returning.
type AuthorizationRepository interface {
	LoadAuthorization(context.Context) (AuthorizationState, error)
	ReplaceAuthorization(context.Context, AuthorizationState) error
}
