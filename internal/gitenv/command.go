// Package gitenv isolates Git commands that explicitly select another repository.
package gitenv

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

// Command runs Git against its explicit arguments, without inheriting Git's
// repository, index, object-store, identity or configuration overrides. In
// particular, -C alone does not override GIT_DIR exported by a Git hook.
// Callers may append intentional Git variables to the returned command's Env.
// This must not be applied to a hook operating on its parent's staged index.
func Command(ctx context.Context, binary string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = clean(os.Environ())
	return cmd
}

func clean(env []string) []string {
	result := make([]string, 0, len(env))
	for _, entry := range env {
		if !strings.HasPrefix(entry, "GIT_") {
			result = append(result, entry)
		}
	}
	return result
}
