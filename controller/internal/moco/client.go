// Package moco implements the controller's small, TLS-only management API adapter.
package moco

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Metadata struct {
	Name        string            `json:"name"`
	Description *string           `json:"description"`
	Labels      map[string]string `json:"labels"`
}

type Resource struct {
	Metadata
	ID         string `json:"id"`
	ExternalID string `json:"externalId"`
	Revision   int64  `json:"revision"`
}

type Policy struct {
	Subject          string `json:"subject"`
	Domain           string `json:"domain"`
	Path             string `json:"path"`
	Method           string `json:"method"`
	SecretPathPrefix string `json:"secretPathPrefix,omitempty"`
}

type RoleBinding struct {
	Principal string `json:"principal"`
	Role      string `json:"role"`
	Domain    string `json:"domain"`
}

type Snapshot struct {
	Policies     []Policy      `json:"policies"`
	RoleBindings []RoleBinding `json:"roleBindings"`
}

type Authorization struct {
	Snapshot
	Revision int64 `json:"revision"`
}

// HTTPError deliberately excludes response bodies, URLs, and credentials.
type HTTPError struct{ Status int }

func (e *HTTPError) Error() string { return fmt.Sprintf("Moco returned HTTP %d", e.Status) }

type Client struct {
	public, admin, token string
	http                 *http.Client
}

func New(public, admin, token string, ca []byte) (*Client, error) {
	for _, endpoint := range []string{public, admin} {
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("moco endpoints must be HTTPS origins without credentials, paths, or queries")
		}
	}
	if strings.TrimSpace(token) == "" || strings.ContainsAny(strings.TrimSpace(token), "\r\n") {
		return nil, errors.New("invalid management token")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("invalid Moco CA bundle")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second, IdleConnTimeout: 30 * time.Second}
	return &Client{public: public, admin: admin, token: strings.TrimSpace(token), http: &http.Client{
		Transport: transport, Timeout: 20 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (c *Client) Close() { c.http.CloseIdleConnections() }

func (c *Client) request(ctx context.Context, internal bool, method, path string, body any, headers map[string]string, result any) (string, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return "", err
		}
		reader = bytes.NewReader(data)
	}
	endpoint := c.public
	if internal {
		endpoint = c.admin
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint+path, reader)
	if err != nil {
		return "", errors.New("cannot construct Moco request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	response, err := c.http.Do(req)
	if err != nil {
		return "", errors.New("moco transport or TLS verification failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", &HTTPError{Status: response.StatusCode}
	}
	if result != nil {
		if err := json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(result); err != nil {
			return "", errors.New("invalid Moco management response")
		}
	}
	etag := response.Header.Get("ETag")

	return etag, nil
}

// Ensure rediscoveries use the immutable external ID, never an unverified status ID.
// A conflict or uncertain mutation is retried by reconciliation after a fresh read.
func (c *Client) Ensure(ctx context.Context, collection, externalID string, desired Metadata) (Resource, error) {
	if desired.Labels == nil {
		desired.Labels = map[string]string{}
	}
	var list struct {
		Items []Resource `json:"items"`
	}
	// Collection responses carry a resourceVersion, not a resource ETag.
	_, err := c.request(ctx, false, "GET", collection+"?externalId="+url.QueryEscape(externalID), nil, nil, &list)
	if err != nil {
		return Resource{}, err
	}
	if len(list.Items) > 1 {
		return Resource{}, errors.New("ambiguous external ID")
	}
	var current Resource
	if len(list.Items) == 0 {
		payload := struct {
			Metadata
			ExternalID string `json:"externalId"`
		}{desired, externalID}
		_, err = c.request(ctx, false, "POST", collection, payload, nil, &current)
		return current, err
	}
	var etag string
	etag, err = c.request(ctx, false, "GET", collection+"/"+url.PathEscape(list.Items[0].ID), nil, nil, &current)
	if err != nil {
		return current, err
	}
	if !strongETag(etag) {
		return current, errors.New("moco response has no strong ETag")
	}
	if current.ExternalID != externalID {
		return current, errors.New("external ID changed during discovery")
	}
	if current.Name == desired.Name && description(current.Description) == description(desired.Description) && maps.Equal(current.Labels, desired.Labels) {
		return current, nil
	}
	_, err = c.request(ctx, false, "PUT", collection+"/"+url.PathEscape(current.ID), desired, map[string]string{"If-Match": etag}, &current)
	return current, err
}

func description(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (c *Client) Authorization(ctx context.Context) (Authorization, string, error) {
	var result Authorization
	etag, err := c.request(ctx, true, "GET", "/internal/v1/authorization", nil, nil, &result)
	if err == nil && !strongETag(etag) {
		err = errors.New("moco response has no strong ETag")
	}
	return result, etag, err
}

func (c *Client) ReplaceAuthorization(ctx context.Context, etag string, snapshot Snapshot) (Authorization, error) {
	var result Authorization
	_, err := c.request(ctx, true, "PUT", "/internal/v1/authorization", snapshot, map[string]string{"If-Match": etag}, &result)
	return result, err
}

func strongETag(etag string) bool {
	return len(etag) >= 2 && strings.HasPrefix(etag, `"`) && strings.HasSuffix(etag, `"`)
}
