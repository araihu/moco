package main

import (
	"bytes"
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
)

const maxAuthorizationConfigBytes = 1 << 20

type authorizationConfiguration struct {
	Principals   []authn.Credential  `json:"principals"`
	RoleBindings []authz.RoleBinding `json:"roleBindings"`
	Policies     []authz.Policy      `json:"policies"`
}

func buildSecurity(configuration configuration) (*authn.TokenAuthenticator, ports.Authorizer, error) {
	if configuration.authConfigPath == "" {
		digest := sha256.Sum256([]byte(configuration.bearerToken))
		principalID := base64.RawURLEncoding.EncodeToString(digest[:])
		authenticator, err := authn.NewTokenAuthenticator([]authn.Credential{{
			PrincipalID: principalID,
			TokenSHA256: hex.EncodeToString(digest[:]),
		}})
		if err != nil {
			return nil, nil, fmt.Errorf("initialize legacy bearer authentication: %w", err)
		}
		authorizer, err := authz.NewStaticAuthorizer(nil, []authz.Policy{
			{Subject: principalID, Domain: "*", Path: "/api/v1", Method: "GET"},
			{Subject: principalID, Domain: "*", Path: "/api/v1/*", Method: "*"},
		})
		if err != nil {
			return nil, nil, fmt.Errorf("initialize legacy authorization: %w", err)
		}
		return authenticator, authorizer, nil
	}

	access, err := loadAuthorizationConfiguration(configuration.authConfigPath)
	if err != nil {
		return nil, nil, err
	}
	authenticator, err := authn.NewTokenAuthenticator(access.Principals)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize configured bearer authentication: %w", err)
	}
	knownPrincipals := make(map[string]struct{}, len(access.Principals))
	for _, principal := range access.Principals {
		knownPrincipals[principal.PrincipalID] = struct{}{}
	}
	for index, binding := range access.RoleBindings {
		if _, ok := knownPrincipals[binding.Principal]; !ok {
			return nil, nil, fmt.Errorf("authorization role binding %d references unknown principal %q", index, binding.Principal)
		}
	}
	authorizer, err := authz.NewStaticAuthorizer(access.RoleBindings, access.Policies)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize configured authorization: %w", err)
	}
	return authenticator, authorizer, nil
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
