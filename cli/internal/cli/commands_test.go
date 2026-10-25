package cli

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMocoCommandsAndDefaultOutput(t *testing.T) {
	commands := [][]string{
		{"service", "info"}, {"service", "watch"},
		{"tenants", "create"}, {"tenants", "get"}, {"tenants", "list"}, {"tenants", "update"}, {"tenants", "delete"}, {"tenants", "watch"},
		{"vaults", "create"}, {"vaults", "get"}, {"vaults", "list"}, {"vaults", "update"}, {"vaults", "delete"},
		{"secrets", "put"}, {"secrets", "get"}, {"secrets", "metadata"}, {"secrets", "list"}, {"secrets", "delete"},
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"items":[{"id":"tenant-1","name":"production"}],"page":{"hasMore":false}}`)
	}))
	defer server.Close()
	t.Setenv("MOCO_TOKEN", "isolated-cli-test-token")
	t.Setenv("MOCO_SERVER", server.URL)
	for _, parts := range commands {
		root, err := New()
		if err != nil {
			t.Fatal(err)
		}
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		root.SetArgs(append(parts, "--help"))
		if err := root.ExecuteContext(t.Context()); err != nil {
			t.Fatalf("command %v: %v", parts, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("command help sent an HTTP request")
	}
	root, err := New()
	if err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"tenants", "list"})
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "id        name\ntenant-1  production\n" || calls.Load() != 1 {
		t.Fatalf("unexpected default table output: %q", stdout.String())
	}
	if !strings.Contains(root.Flag("output").Usage, "csv") || root.Flag("output").DefValue != "table" {
		t.Fatal("output formats or table default are missing")
	}
}
