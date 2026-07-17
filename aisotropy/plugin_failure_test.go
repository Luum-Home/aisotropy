package aisotropy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestStartRuntime_BadPluginPathsSkipped covers the best-effort plugin loading
// branches in startRuntime: a WithPlugin file that fails to parse and a
// WithPluginDir that fails to scan are each logged and skipped, never downing
// the runtime — a Turn still completes.
func TestStartRuntime_BadPluginPathsSkipped(t *testing.T) {
	badManifest := filepath.Join(t.TempDir(), "broken.yaml")
	if err := os.WriteFile(badManifest, []byte("this: : not: valid: yaml: ["), 0o600); err != nil {
		t.Fatalf("write bad manifest: %v", err)
	}

	app := New(
		WithLLM(fastStub()),
		WithRateLimit(1e9, 1e9),
		WithPlugin(badManifest),
		WithPluginDir(filepath.Join(t.TempDir(), "does-not-exist-dir")),
	)
	defer closeApp(t, app)()

	if _, err := app.Turn(context.Background(), ChatInput{
		TenantID: "acme", UserID: "alice", SessionID: "s1", Prompt: "hi",
	}); err != nil {
		t.Fatalf("Turn must survive bad plugin paths: %v", err)
	}
}

// TestStartRuntime_PluginDirFromEnv covers the AISOTROPY_PLUGIN_DIR branch: a
// valid go-import manifest discovered via the env directory contributes its
// scope-gated tool into the runtime registry, exactly like WithPluginDir.
func TestStartRuntime_PluginDirFromEnv(t *testing.T) {
	dir := t.TempDir()
	manifest := "" +
		"name: env-plugin\n" +
		"version: 0.0.1\n" +
		"license: MIT\n" +
		"kind: go-import\n" +
		"tools:\n" +
		"  - name: env_plugin_tool\n" +
		"    description: tool from an env-dir plugin\n"
	if err := os.WriteFile(filepath.Join(dir, "aisotropy-plugin.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	t.Setenv("AISOTROPY_PLUGIN_DIR", dir)

	app := New(WithLLM(fastStub()), WithRateLimit(1e9, 1e9))
	defer closeApp(t, app)()

	rt, err := app.ensureRuntime()
	if err != nil {
		t.Fatalf("ensureRuntime: %v", err)
	}
	found := false
	for _, tool := range rt.toolRegistry.Permitted(nil) {
		if tool.Name == "env_plugin_tool" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("AISOTROPY_PLUGIN_DIR manifest tool was not merged into the registry")
	}
}
