package ports

import "context"

// Authorizer evaluates one authenticated principal against an API resource.
// Implementations must fail closed when no policy matches.
type Authorizer interface {
	Authorize(context.Context, string, string, string, string) (bool, error)
}
