package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/araihu/moco/internal/adapters/authn"
	"github.com/araihu/moco/internal/adapters/authz"
	"github.com/araihu/moco/internal/core/ports"
	"github.com/araihu/moco/internal/core/services"
)

const maxAuthorizationConfigBytes = 1 << 20

type authorizationConfiguration struct {
	Principals   []authn.Credential  `json:"principals"`
	RoleBindings []authz.RoleBinding `json:"roleBindings"`
	Policies     []authz.Policy      `json:"policies"`
}

type securityRuntime struct {
	authenticator *authn.TokenAuthenticator
	authorizer    *authz.StaticAuthorizer
	policyService *services.AuthorizationPolicyService
	reloader      *authz.PolicyReloader
	bus           *authz.MemoryPolicyChangesBus
}

func (runtime securityRuntime) close() {
	if runtime.bus != nil {
		runtime.bus.Close()
	}
}

func buildSecurity(configuration configuration) (*authn.TokenAuthenticator, ports.Authorizer, error) {
	runtime, err := buildSecurityRuntime(context.Background(), configuration, nil)
	if err != nil {
		return nil, nil, err
	}
	return runtime.authenticator, runtime.authorizer, nil
}

func buildSecurityRuntime(ctx context.Context, configuration configuration, repository ports.AuthorizationRepository) (securityRuntime, error) {
	if err := ctx.Err(); err != nil {
		return securityRuntime{}, err
	}
	if configuration.authConfigPath == "" {
		digest := sha256.Sum256([]byte(configuration.bearerToken))
		principalID := base64.RawURLEncoding.EncodeToString(digest[:])
		authenticator, err := authn.NewTokenAuthenticator([]authn.Credential{{
			PrincipalID: principalID,
			TokenSHA256: hex.EncodeToString(digest[:]),
		}})
		if err != nil {
			return securityRuntime{}, fmt.Errorf("initialize legacy bearer authentication: %w", err)
		}
		authorizer, err := authz.NewStaticAuthorizer(nil, []authz.Policy{
			{Subject: principalID, Domain: "*", Path: "/api/v1", Method: "GET"},
			{Subject: principalID, Domain: "*", Path: "/api/v1/*", Method: "*"},
			{Subject: principalID, Domain: "*", Path: "/internal/v1/authorization", Method: "GET"},
			{Subject: principalID, Domain: "*", Path: "/internal/v1/authorization", Method: "PUT"},
			{Subject: principalID, Domain: "*", Path: "/internal/v1/audit", Method: "GET"},
			{Subject: principalID, Domain: "*", Path: "/internal/v1/encryption/rotation", Method: "POST"},
		})
		if err != nil {
			return securityRuntime{}, fmt.Errorf("initialize legacy authorization: %w", err)
		}
		return securityRuntime{authenticator: authenticator, authorizer: authorizer}, nil
	}

	access, err := loadAuthorizationConfiguration(configuration.authConfigPath)
	if err != nil {
		return securityRuntime{}, err
	}
	authenticator, err := authn.NewTokenAuthenticator(access.Principals)
	if err != nil {
		return securityRuntime{}, fmt.Errorf("initialize configured bearer authentication: %w", err)
	}
	roleBindings := access.RoleBindings
	policies := access.Policies
	var policyService *services.AuthorizationPolicyService
	var reloader *authz.PolicyReloader
	var bus *authz.MemoryPolicyChangesBus
	if repository != nil {
		state, loadErr := repository.LoadAuthorization(ctx)
		if loadErr != nil {
			return securityRuntime{}, fmt.Errorf("load persisted authorization: %w", loadErr)
		}
		bus = authz.NewMemoryPolicyChangesBus()
		var serviceErr error
		policyService, serviceErr = services.NewAuthorizationPolicyService(repository, bus)
		if serviceErr != nil {
			bus.Close()
			return securityRuntime{}, fmt.Errorf("initialize authorization policy service: %w", serviceErr)
		}
		if !state.Initialized {
			if err := validateRoleBindings(access.Principals, roleBindings); err != nil {
				bus.Close()
				return securityRuntime{}, err
			}
			// Validate the file-provided candidate before it can be persisted.
			// This keeps a malformed deployment from poisoning the snapshot.
			if _, err := authz.NewStaticAuthorizer(roleBindings, policies); err != nil {
				bus.Close()
				return securityRuntime{}, fmt.Errorf("initialize configured authorization: %w", err)
			}
			seed := ports.AuthorizationState{Revision: state.Revision, RoleBindings: roleBindings, Policies: policies}
			if replaceErr := policyService.ReplaceAuthorization(ctx, seed); replaceErr != nil {
				bus.Close()
				return securityRuntime{}, fmt.Errorf("bootstrap persisted authorization: %w", replaceErr)
			}
			state, loadErr = repository.LoadAuthorization(ctx)
			if loadErr != nil {
				bus.Close()
				return securityRuntime{}, fmt.Errorf("reload bootstrapped authorization: %w", loadErr)
			}
		}
		roleBindings = state.RoleBindings
		policies = state.Policies
		if err := validateRoleBindings(access.Principals, roleBindings); err != nil {
			bus.Close()
			return securityRuntime{}, fmt.Errorf("validate persisted authorization: %w", err)
		}
	} else {
		if err := validateRoleBindings(access.Principals, roleBindings); err != nil {
			return securityRuntime{}, err
		}
	}
	authorizer, err := authz.NewStaticAuthorizer(roleBindings, policies)
	if err != nil {
		if bus != nil {
			bus.Close()
		}
		return securityRuntime{}, fmt.Errorf("initialize configured authorization: %w", err)
	}
	if repository != nil {
		reloader, err = authz.NewPolicyReloader(authorizer, repository, bus)
		if err != nil {
			bus.Close()
			return securityRuntime{}, fmt.Errorf("initialize authorization policy reloader: %w", err)
		}
	}
	return securityRuntime{authenticator: authenticator, authorizer: authorizer, policyService: policyService, reloader: reloader, bus: bus}, nil
}

func validateRoleBindings(principals []authn.Credential, bindings []authz.RoleBinding) error {
	knownPrincipals := make(map[string]struct{}, len(principals))
	for _, principal := range principals {
		knownPrincipals[principal.PrincipalID] = struct{}{}
	}
	for index, binding := range bindings {
		if _, ok := knownPrincipals[binding.Principal]; !ok {
			return fmt.Errorf("authorization role binding %d references unknown principal %q", index, binding.Principal)
		}
	}
	return nil
}

func loadAuthorizationConfiguration(path string) (authorizationConfiguration, error) {
	if path == "" {
		return authorizationConfiguration{}, errors.New("authorization configuration path is required")
	}
	file, err := os.Open(path) // #nosec G304 -- path is an explicit deployment setting.
	if err != nil {
		return authorizationConfiguration{}, fmt.Errorf("open authorization configuration: %w", err)
	}
	defer func() { _ = file.Close() }()
	payload, err := io.ReadAll(io.LimitReader(file, maxAuthorizationConfigBytes+1))
	if err != nil {
		return authorizationConfiguration{}, fmt.Errorf("read authorization configuration: %w", err)
	}
	if len(payload) > maxAuthorizationConfigBytes {
		return authorizationConfiguration{}, errors.New("authorization configuration exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var configuration authorizationConfiguration
	if err := decoder.Decode(&configuration); err != nil {
		return authorizationConfiguration{}, fmt.Errorf("decode authorization configuration: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return authorizationConfiguration{}, errors.New("authorization configuration must contain exactly one JSON value")
	}
	if len(configuration.Principals) == 0 {
		return authorizationConfiguration{}, errors.New("authorization configuration requires at least one principal")
	}
	return configuration, nil
}
