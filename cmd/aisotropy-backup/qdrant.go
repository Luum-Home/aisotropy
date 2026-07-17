package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"time"
)

// qdrantHTTPTimeout bounds each REST call. Snapshot creation on a large
// collection can take a while, so this is generous; override by editing here if
// a collection is very large (documented in backup-restore.md).
const qdrantHTTPTimeout = 10 * time.Minute

// qdrantConn holds the resolved REST connection parameters. The semantic tier
// talks gRPC on 6334; the SNAPSHOT API is REST-only, so this defaults to the
// REST port 6333 (not the gRPC port the app uses).
type qdrantConn struct {
	base   string // e.g. http://localhost:6333
	apiKey string
	client *http.Client
}

// resolveQdrant builds a qdrantConn from flags with env defaults.
func resolveQdrant(host string, port int, tls bool, apiKey string) (qdrantConn, error) {
	if host == "" {
		return qdrantConn{}, fmt.Errorf("qdrant: --host (or $QDRANT_HOST) is required")
	}
	scheme := "http"
	if tls {
		scheme = "https"
	}
	return qdrantConn{
		base:   fmt.Sprintf("%s://%s:%d", scheme, host, port),
		apiKey: apiKey,
		client: &http.Client{Timeout: qdrantHTTPTimeout},
	}, nil
}

func (q qdrantConn) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, q.base+path, body)
	if err != nil {
		return nil, err
	}
	if q.apiKey != "" {
		req.Header.Set("api-key", q.apiKey)
	}
	return req, nil
}

// qdrantSnapshotResult is the subset of Qdrant's snapshot-create response we
// need: the generated snapshot name to download.
type qdrantSnapshotResult struct {
	Result struct {
		Name string `json:"name"`
	} `json:"result"`
	Status string `json:"status"`
}

// qdrantSnapshot creates a snapshot of a collection via the REST API and
// downloads it to --out. Two REST calls: POST .../snapshots?wait=true to create,
// then GET .../snapshots/{name} to stream the archive to disk.
func qdrantSnapshot(ctx context.Context, args []string) error {
	fs := newFlagSet("qdrant snapshot")
	host := fs.String("host", os.Getenv("QDRANT_HOST"), "Qdrant host (default $QDRANT_HOST)")
	port := fs.Int("port", envInt("QDRANT_REST_PORT", 6333), "Qdrant REST port (default 6333; NOT the 6334 gRPC port)")
	tls := fs.Bool("tls", os.Getenv("QDRANT_USE_TLS") == "true", "use HTTPS")
	apiKey := fs.String("api-key", os.Getenv("QDRANT_API_KEY"), "Qdrant API key (default $QDRANT_API_KEY)")
	collection := fs.String("collection", os.Getenv("QDRANT_COLLECTION"), "collection name (required; default $QDRANT_COLLECTION)")
	out := fs.String("out", "", "output snapshot file path (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *collection == "" {
		return fmt.Errorf("qdrant snapshot: --collection (or $QDRANT_COLLECTION) is required")
	}
	if *out == "" {
		return fmt.Errorf("qdrant snapshot: --out is required")
	}
	q, err := resolveQdrant(*host, *port, *tls, *apiKey)
	if err != nil {
		return err
	}

	// 1) Create the snapshot (wait=true so the name we read is materialized).
	createReq, err := q.newRequest(ctx, http.MethodPost, "/collections/"+*collection+"/snapshots?wait=true", nil)
	if err != nil {
		return err
	}
	createResp, err := q.client.Do(createReq)
	if err != nil {
		return fmt.Errorf("qdrant snapshot: create request: %w", err)
	}
	defer createResp.Body.Close()
	if createResp.StatusCode != http.StatusOK {
		return fmt.Errorf("qdrant snapshot: create returned %s: %s", createResp.Status, readErrBody(createResp.Body))
	}
	var created qdrantSnapshotResult
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		return fmt.Errorf("qdrant snapshot: decode create response: %w", err)
	}
	if created.Result.Name == "" {
		return fmt.Errorf("qdrant snapshot: create returned no snapshot name (status %q)", created.Status)
	}

	// 2) Download the snapshot archive.
	dlReq, err := q.newRequest(ctx, http.MethodGet, "/collections/"+*collection+"/snapshots/"+created.Result.Name, nil)
	if err != nil {
		return err
	}
	dlResp, err := q.client.Do(dlReq)
	if err != nil {
		return fmt.Errorf("qdrant snapshot: download request: %w", err)
	}
	defer dlResp.Body.Close()
	if dlResp.StatusCode != http.StatusOK {
		return fmt.Errorf("qdrant snapshot: download returned %s: %s", dlResp.Status, readErrBody(dlResp.Body))
	}
	f, err := os.Create(*out)
	if err != nil {
		return fmt.Errorf("qdrant snapshot: create %q: %w", *out, err)
	}
	defer f.Close()
	if _, err := io.Copy(f, dlResp.Body); err != nil {
		return fmt.Errorf("qdrant snapshot: write %q: %w", *out, err)
	}
	fmt.Fprintf(os.Stderr, "qdrant snapshot: downloaded %q -> %q\n", created.Result.Name, *out)
	return nil
}

// qdrantRestore uploads a local snapshot file into a collection via the REST
// upload endpoint (multipart form field "snapshot"), with priority=snapshot so
// the snapshot's data wins over whatever is currently in the collection.
func qdrantRestore(ctx context.Context, args []string) error {
	fs := newFlagSet("qdrant restore")
	host := fs.String("host", os.Getenv("QDRANT_HOST"), "Qdrant host (default $QDRANT_HOST)")
	port := fs.Int("port", envInt("QDRANT_REST_PORT", 6333), "Qdrant REST port (default 6333)")
	tls := fs.Bool("tls", os.Getenv("QDRANT_USE_TLS") == "true", "use HTTPS")
	apiKey := fs.String("api-key", os.Getenv("QDRANT_API_KEY"), "Qdrant API key (default $QDRANT_API_KEY)")
	collection := fs.String("collection", os.Getenv("QDRANT_COLLECTION"), "target collection name (required)")
	in := fs.String("in", "", "input snapshot file to upload (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *collection == "" {
		return fmt.Errorf("qdrant restore: --collection (or $QDRANT_COLLECTION) is required")
	}
	if *in == "" {
		return fmt.Errorf("qdrant restore: --in is required")
	}
	q, err := resolveQdrant(*host, *port, *tls, *apiKey)
	if err != nil {
		return err
	}

	src, err := os.Open(*in)
	if err != nil {
		return fmt.Errorf("qdrant restore: open %q: %w", *in, err)
	}
	defer src.Close()

	// Stream the multipart body through a pipe so a large snapshot is never
	// buffered wholly in memory.
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		part, perr := mw.CreateFormFile("snapshot", filepathBase(*in))
		if perr != nil {
			pw.CloseWithError(perr)
			return
		}
		if _, perr := io.Copy(part, src); perr != nil {
			pw.CloseWithError(perr)
			return
		}
		pw.CloseWithError(mw.Close())
	}()

	req, err := q.newRequest(ctx, http.MethodPost, "/collections/"+*collection+"/snapshots/upload?priority=snapshot", pr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := q.client.Do(req)
	if err != nil {
		return fmt.Errorf("qdrant restore: upload request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("qdrant restore: upload returned %s: %s", resp.Status, readErrBody(resp.Body))
	}
	fmt.Fprintf(os.Stderr, "qdrant restore: uploaded %q into collection %q\n", *in, *collection)
	return nil
}

// envInt reads key as an int, falling back to def when unset or unparsable.
func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// readErrBody reads up to 2KB of an error response body for diagnostics.
func readErrBody(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 2<<10))
	if len(b) == 0 {
		return "(empty body)"
	}
	return string(b)
}

// filepathBase returns the final path element without importing path/filepath
// for a single call — keeps the multipart form filename clean.
func filepathBase(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[i+1:]
		}
	}
	return p
}
