package main

import (
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/araihu/moco/internal/operations"
)

func TestProbeValidatesTLSAndReadiness(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" {
			w.WriteHeader(200)
		} else {
			w.WriteHeader(503)
		}
	}))
	defer server.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), []string{"probe", "--url", server.URL + "/readyz", "--ca", ca}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := probe(t.Context(), server.URL+"/unready", ca); err == nil {
		t.Fatal("unready server accepted")
	}
	if err := probe(t.Context(), "http://localhost/readyz", ca); err == nil {
		t.Fatal("plaintext health probe accepted")
	}
	if err := probe(t.Context(), strings.Replace(server.URL, "127.0.0.1", "localhost", 1), ca); err == nil {
		t.Fatal("TLS hostname mismatch accepted")
	}
}

func TestBackupHealthRejectsFailureAndStaleSnapshot(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "status.json")
	for _, status := range []operations.BackupStatus{
		{},
		{LastAttemptOK: false, SnapshotAt: time.Now()},
		{LastAttemptOK: true, SnapshotAt: time.Now().Add(-time.Hour)},
		{LastAttemptOK: true, SnapshotAt: time.Now().Add(time.Hour)},
		{LastAttemptOK: true, SnapshotAt: time.Now()},
	} {
		payload, err := json.Marshal(status)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			t.Fatal(err)
		}
		err = run(t.Context(), []string{"backup-health", "--status", path}, io.Discard, io.Discard)
		wantOK := status.LastAttemptOK && time.Since(status.SnapshotAt) >= 0 && time.Since(status.SnapshotAt) < time.Minute
		if (err == nil) != wantOK {
			t.Fatalf("incorrect backup health for %+v: %v", status, err)
		}
	}
}
