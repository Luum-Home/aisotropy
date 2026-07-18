package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// --- resolveQdrant / newRequest -----------------------------------------------

func TestResolveQdrantMissingHost(t *testing.T) {
	if _, err := resolveQdrant("", 6333, false, ""); err == nil {
		t.Fatal("err = nil, want a --host required error")
	}
}

func TestResolveQdrantSchemeSelection(t *testing.T) {
	plain, err := resolveQdrant("localhost", 6333, false, "")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if plain.base != "http://localhost:6333" {
		t.Errorf("base = %q, want http scheme", plain.base)
	}

	secure, err := resolveQdrant("localhost", 6333, true, "key123")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if secure.base != "https://localhost:6333" {
		t.Errorf("base = %q, want https scheme", secure.base)
	}
	if secure.apiKey != "key123" {
		t.Errorf("apiKey = %q, want %q", secure.apiKey, "key123")
	}
}

func TestNewRequestSetsAPIKeyHeader(t *testing.T) {
	q := qdrantConn{base: "http://example.invalid", apiKey: "secret-key", client: http.DefaultClient}
	req, err := q.newRequest(context.Background(), http.MethodGet, "/x", nil)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got := req.Header.Get("api-key"); got != "secret-key" {
		t.Errorf("api-key header = %q, want %q", got, "secret-key")
	}
}

func TestNewRequestNoAPIKeyHeaderWhenUnset(t *testing.T) {
	q := qdrantConn{base: "http://example.invalid", client: http.DefaultClient}
	req, err := q.newRequest(context.Background(), http.MethodGet, "/x", nil)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got := req.Header.Get("api-key"); got != "" {
		t.Errorf("api-key header = %q, want empty", got)
	}
}

func TestNewRequestPropagatesInvalidRequestError(t *testing.T) {
	q := qdrantConn{base: "http://example.invalid", client: http.DefaultClient}
	// An invalid HTTP method (contains a space) makes
	// http.NewRequestWithContext return an error deterministically.
	_, err := q.newRequest(context.Background(), "IN VALID", "/x", nil)
	if err == nil {
		t.Fatal("err = nil, want an invalid-method error")
	}
}

// --- test doubles for the Qdrant REST surface -----------------------------

// fakeQdrant builds an httptest.Server simulating just enough of Qdrant's
// snapshot REST API for qdrantSnapshot/qdrantRestore to round-trip against.
func fakeQdrant(t *testing.T, snapshotName string, snapshotBody []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/collections/mem/snapshots", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "want POST", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"result":{"name":%q},"status":"ok"}`, snapshotName)
	})
	mux.HandleFunc("/collections/mem/snapshots/"+snapshotName, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "want GET", http.StatusMethodNotAllowed)
			return
		}
		w.Write(snapshotBody)
	})
	mux.HandleFunc("/collections/mem/snapshots/upload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "want POST", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, "bad multipart: "+err.Error(), http.StatusBadRequest)
			return
		}
		file, _, err := r.FormFile("snapshot")
		if err != nil {
			http.Error(w, "missing snapshot field: "+err.Error(), http.StatusBadRequest)
			return
		}
		defer file.Close()
		got, _ := io.ReadAll(file)
		if string(got) != string(snapshotBody) {
			http.Error(w, "uploaded content mismatch", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return httptest.NewServer(mux)
}

// hostPort splits an httptest server URL into flag-ready host/port strings.
func hostPort(t *testing.T, rawURL string) (string, int) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse test server port: %v", err)
	}
	return u.Hostname(), port
}

// --- qdrantSnapshot ------------------------------------------------------------

func TestQdrantSnapshotFlagParseError(t *testing.T) {
	err := qdrantSnapshot(context.Background(), []string{"--not-a-real-flag=1"})
	if err == nil {
		t.Fatal("err = nil, want a flag-parse error")
	}
}

// TestQdrantSnapshotCreateRequestInvalidURL embeds a control character in
// --collection so the create request's underlying URL fails to parse,
// covering newRequest's error-propagation branch inside qdrantSnapshot.
func TestQdrantSnapshotCreateRequestInvalidURL(t *testing.T) {
	err := qdrantSnapshot(context.Background(), []string{
		"--host", "localhost", "--collection", "mem\nbad", "--out", filepath.Join(t.TempDir(), "out"),
	})
	if err == nil {
		t.Fatal("err = nil, want a request-construction error")
	}
}

// TestQdrantSnapshotCreateRequestDialRefused points at a closed local port so
// the create request's client.Do fails at the network layer.
func TestQdrantSnapshotCreateRequestDialRefused(t *testing.T) {
	err := qdrantSnapshot(context.Background(), []string{
		"--host", "127.0.0.1", "--port", "1", "--collection", "mem",
		"--out", filepath.Join(t.TempDir(), "out"),
	})
	if err == nil || !strings.Contains(err.Error(), "create request") {
		t.Errorf("err = %v, want a create-request error", err)
	}
}

// TestQdrantSnapshotDownloadRequestInvalidURL has the fake server return a
// snapshot name containing a control character, so the SECOND newRequest
// call (for the download) fails to parse its URL while the first (create)
// call succeeds normally.
func TestQdrantSnapshotDownloadRequestInvalidURL(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/collections/mem/snapshots", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"result":{"name":"bad\nname"},"status":"ok"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	host, port := hostPort(t, srv.URL)

	err := qdrantSnapshot(context.Background(), []string{
		"--host", host, "--port", strconv.Itoa(port), "--collection", "mem",
		"--out", filepath.Join(t.TempDir(), "out"),
	})
	if err == nil {
		t.Fatal("err = nil, want a download-request-construction error")
	}
}

// TestQdrantSnapshotDownloadRequestDoFailure lets the create call succeed
// normally, then hijacks and abruptly closes the connection during the
// download call so client.Do fails at the network layer for that leg
// specifically (distinct from TestQdrantSnapshotCreateRequestDialRefused).
func TestQdrantSnapshotDownloadRequestDoFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/collections/mem/snapshots", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"result":{"name":"snap-1"},"status":"ok"}`)
	})
	mux.HandleFunc("/collections/mem/snapshots/snap-1", func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			return
		}
		conn.Close() // abrupt close, no response at all
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	host, port := hostPort(t, srv.URL)

	err := qdrantSnapshot(context.Background(), []string{
		"--host", host, "--port", strconv.Itoa(port), "--collection", "mem",
		"--out", filepath.Join(t.TempDir(), "out"),
	})
	if err == nil || !strings.Contains(err.Error(), "download request") {
		t.Errorf("err = %v, want a download-request error", err)
	}
}

func TestQdrantSnapshotMissingCollection(t *testing.T) {
	t.Setenv("QDRANT_COLLECTION", "")
	err := qdrantSnapshot(context.Background(), []string{"--host", "localhost", "--out", "x"})
	if err == nil || !strings.Contains(err.Error(), "--collection") {
		t.Errorf("err = %v, want a --collection required error", err)
	}
}

func TestQdrantSnapshotMissingOut(t *testing.T) {
	err := qdrantSnapshot(context.Background(), []string{"--host", "localhost", "--collection", "mem"})
	if err == nil || !strings.Contains(err.Error(), "--out") {
		t.Errorf("err = %v, want an --out required error", err)
	}
}

func TestQdrantSnapshotMissingHost(t *testing.T) {
	t.Setenv("QDRANT_HOST", "")
	err := qdrantSnapshot(context.Background(), []string{"--collection", "mem", "--out", "x"})
	if err == nil || !strings.Contains(err.Error(), "--host") {
		t.Errorf("err = %v, want a --host required error", err)
	}
}

func TestQdrantSnapshotSuccess(t *testing.T) {
	snapshotBody := []byte("fake qdrant snapshot archive bytes")
	srv := fakeQdrant(t, "snap-2026", snapshotBody)
	defer srv.Close()
	host, port := hostPort(t, srv.URL)

	out := filepath.Join(t.TempDir(), "out.snapshot")
	err := qdrantSnapshot(context.Background(), []string{
		"--host", host, "--port", strconv.Itoa(port), "--collection", "mem", "--out", out,
	})
	if err != nil {
		t.Fatalf("qdrantSnapshot: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(snapshotBody) {
		t.Errorf("downloaded snapshot = %q, want %q", got, snapshotBody)
	}
}

func TestQdrantSnapshotCreateNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	host, port := hostPort(t, srv.URL)

	err := qdrantSnapshot(context.Background(), []string{
		"--host", host, "--port", strconv.Itoa(port), "--collection", "mem",
		"--out", filepath.Join(t.TempDir(), "out.snapshot"),
	})
	if err == nil || !strings.Contains(err.Error(), "create returned") {
		t.Errorf("err = %v, want a create-returned error", err)
	}
}

func TestQdrantSnapshotCreateBadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("{not json"))
	}))
	defer srv.Close()
	host, port := hostPort(t, srv.URL)

	err := qdrantSnapshot(context.Background(), []string{
		"--host", host, "--port", strconv.Itoa(port), "--collection", "mem",
		"--out", filepath.Join(t.TempDir(), "out.snapshot"),
	})
	if err == nil || !strings.Contains(err.Error(), "decode create response") {
		t.Errorf("err = %v, want a decode-create-response error", err)
	}
}

func TestQdrantSnapshotCreateNoName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"result":{"name":""},"status":"ok"}`)
	}))
	defer srv.Close()
	host, port := hostPort(t, srv.URL)

	err := qdrantSnapshot(context.Background(), []string{
		"--host", host, "--port", strconv.Itoa(port), "--collection", "mem",
		"--out", filepath.Join(t.TempDir(), "out.snapshot"),
	})
	if err == nil || !strings.Contains(err.Error(), "no snapshot name") {
		t.Errorf("err = %v, want a no-snapshot-name error", err)
	}
}

func TestQdrantSnapshotDownloadNon200(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/collections/mem/snapshots", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"result":{"name":"snap-x"},"status":"ok"}`)
	})
	mux.HandleFunc("/collections/mem/snapshots/snap-x", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	host, port := hostPort(t, srv.URL)

	err := qdrantSnapshot(context.Background(), []string{
		"--host", host, "--port", strconv.Itoa(port), "--collection", "mem",
		"--out", filepath.Join(t.TempDir(), "out.snapshot"),
	})
	if err == nil || !strings.Contains(err.Error(), "download returned") {
		t.Errorf("err = %v, want a download-returned error", err)
	}
}

func TestQdrantSnapshotOutCreateFailure(t *testing.T) {
	srv := fakeQdrant(t, "snap-y", []byte("bytes"))
	defer srv.Close()
	host, port := hostPort(t, srv.URL)

	// Pointing --out at an existing directory makes os.Create fail.
	err := qdrantSnapshot(context.Background(), []string{
		"--host", host, "--port", strconv.Itoa(port), "--collection", "mem",
		"--out", t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "create") {
		t.Errorf("err = %v, want a create-file error", err)
	}
}

// --- qdrantRestore ---------------------------------------------------------

func TestQdrantRestoreFlagParseError(t *testing.T) {
	err := qdrantRestore(context.Background(), []string{"--not-a-real-flag=1"})
	if err == nil {
		t.Fatal("err = nil, want a flag-parse error")
	}
}

// TestQdrantRestoreUploadRequestInvalidURL embeds a control character in
// --collection so the upload request's underlying URL fails to parse.
func TestQdrantRestoreUploadRequestInvalidURL(t *testing.T) {
	in := filepath.Join(t.TempDir(), "in")
	if err := os.WriteFile(in, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := qdrantRestore(context.Background(), []string{
		"--host", "localhost", "--collection", "mem\nbad", "--in", in,
	})
	if err == nil {
		t.Fatal("err = nil, want a request-construction error")
	}
}

// TestQdrantRestoreUploadRequestDialRefused points at a closed local port so
// the upload request's client.Do fails at the network layer.
func TestQdrantRestoreUploadRequestDialRefused(t *testing.T) {
	in := filepath.Join(t.TempDir(), "in")
	if err := os.WriteFile(in, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := qdrantRestore(context.Background(), []string{
		"--host", "127.0.0.1", "--port", "1", "--collection", "mem", "--in", in,
	})
	if err == nil || !strings.Contains(err.Error(), "upload request") {
		t.Errorf("err = %v, want an upload-request error", err)
	}
}

func TestQdrantRestoreMissingCollection(t *testing.T) {
	t.Setenv("QDRANT_COLLECTION", "")
	err := qdrantRestore(context.Background(), []string{"--host", "localhost", "--in", "x"})
	if err == nil || !strings.Contains(err.Error(), "--collection") {
		t.Errorf("err = %v, want a --collection required error", err)
	}
}

func TestQdrantRestoreMissingIn(t *testing.T) {
	err := qdrantRestore(context.Background(), []string{"--host", "localhost", "--collection", "mem"})
	if err == nil || !strings.Contains(err.Error(), "--in") {
		t.Errorf("err = %v, want an --in required error", err)
	}
}

func TestQdrantRestoreMissingHost(t *testing.T) {
	t.Setenv("QDRANT_HOST", "")
	err := qdrantRestore(context.Background(), []string{"--collection", "mem", "--in", "x"})
	if err == nil || !strings.Contains(err.Error(), "--host") {
		t.Errorf("err = %v, want a --host required error", err)
	}
}

func TestQdrantRestoreInputNotReadable(t *testing.T) {
	err := qdrantRestore(context.Background(), []string{
		"--host", "localhost", "--collection", "mem",
		"--in", filepath.Join(t.TempDir(), "does-not-exist"),
	})
	if err == nil || !strings.Contains(err.Error(), "open") {
		t.Errorf("err = %v, want an open error for a missing input file", err)
	}
}

func TestQdrantRestoreSuccess(t *testing.T) {
	snapshotBody := []byte("fake qdrant snapshot archive bytes for restore")
	srv := fakeQdrant(t, "snap-restore", snapshotBody)
	defer srv.Close()
	host, port := hostPort(t, srv.URL)

	in := filepath.Join(t.TempDir(), "in.snapshot")
	if err := os.WriteFile(in, snapshotBody, 0o600); err != nil {
		t.Fatal(err)
	}

	err := qdrantRestore(context.Background(), []string{
		"--host", host, "--port", strconv.Itoa(port), "--collection", "mem", "--in", in,
	})
	if err != nil {
		t.Fatalf("qdrantRestore: %v", err)
	}
}

func TestQdrantRestoreUploadNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rejected", http.StatusBadRequest)
	}))
	defer srv.Close()
	host, port := hostPort(t, srv.URL)

	in := filepath.Join(t.TempDir(), "in.snapshot")
	if err := os.WriteFile(in, []byte("bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := qdrantRestore(context.Background(), []string{
		"--host", host, "--port", strconv.Itoa(port), "--collection", "mem", "--in", in,
	})
	if err == nil || !strings.Contains(err.Error(), "upload returned") {
		t.Errorf("err = %v, want an upload-returned error", err)
	}
}

// --- envInt ----------------------------------------------------------------

func TestEnvIntUnsetUsesDefault(t *testing.T) {
	t.Setenv("AISOTROPY_TEST_ENVINT", "")
	if got := envInt("AISOTROPY_TEST_ENVINT", 42); got != 42 {
		t.Errorf("envInt = %d, want default 42", got)
	}
}

func TestEnvIntValidValue(t *testing.T) {
	t.Setenv("AISOTROPY_TEST_ENVINT", "7")
	if got := envInt("AISOTROPY_TEST_ENVINT", 42); got != 7 {
		t.Errorf("envInt = %d, want 7", got)
	}
}

func TestEnvIntUnparsableFallsBackToDefault(t *testing.T) {
	t.Setenv("AISOTROPY_TEST_ENVINT", "not-a-number")
	if got := envInt("AISOTROPY_TEST_ENVINT", 42); got != 42 {
		t.Errorf("envInt = %d, want default 42 for unparsable value", got)
	}
}

// --- readErrBody -------------------------------------------------------------

func TestReadErrBodyNormal(t *testing.T) {
	got := readErrBody(strings.NewReader("some error text"))
	if got != "some error text" {
		t.Errorf("readErrBody = %q, want %q", got, "some error text")
	}
}

func TestReadErrBodyEmpty(t *testing.T) {
	got := readErrBody(strings.NewReader(""))
	if got != "(empty body)" {
		t.Errorf("readErrBody = %q, want %q", got, "(empty body)")
	}
}

// --- filepathBase ------------------------------------------------------------

func TestFilepathBase(t *testing.T) {
	cases := map[string]string{
		"/a/b/c.snapshot":   "c.snapshot",
		`C:\a\b\c.snapshot`: "c.snapshot",
		"nofile-slash":      "nofile-slash",
		"":                  "",
	}
	for in, want := range cases {
		if got := filepathBase(in); got != want {
			t.Errorf("filepathBase(%q) = %q, want %q", in, got, want)
		}
	}
}
