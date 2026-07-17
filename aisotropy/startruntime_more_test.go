package aisotropy

import (
	"context"
	"net"
	"testing"
	"time"
)

// TestStartRuntime_BadMCPServerSkipped covers the best-effort MCP connect branch
// in startRuntime: a WithMCPServer pointing at a nonexistent command fails to
// connect and is logged + skipped, never downing the runtime — a Turn still
// completes over the (unchanged) native toolset.
func TestStartRuntime_BadMCPServerSkipped(t *testing.T) {
	app := New(
		WithLLM(fastStub()),
		WithRateLimit(1e9, 1e9),
		WithMCPServer("read:files", "aisotropy-nonexistent-mcp-binary-xyz"),
	)
	defer closeApp(t, app)()

	out, err := app.Turn(context.Background(), ChatInput{
		TenantID: "acme", UserID: "alice", SessionID: "s1", Prompt: "hello",
	})
	if err != nil {
		t.Fatalf("Turn must still succeed when an MCP server fails to connect: %v", err)
	}
	if out.TenantID != "acme" {
		t.Errorf("tenant echo = %q, want acme", out.TenantID)
	}
}

// TestRun_ServerStartFailureReturnsError covers Run's server-start-failure
// branch: binding to an address already in use makes ListenAndServe fail, which
// Run surfaces as an error (after tearing the runtime down) rather than hanging.
func TestRun_ServerStartFailureReturnsError(t *testing.T) {
	// Hold a listener on a loopback port so Run's bind collides.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer func() { _ = ln.Close() }()
	addr := ln.Addr().String()

	app := New(WithAddr(addr), WithLLM(fastStub()), WithAuthenticator(NoAuth()))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("Run must return an error when the address is already in use")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after a bind failure")
	}
}
