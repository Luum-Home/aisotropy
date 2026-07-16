//go:build wasip1

// Command echomod is a TEST FIXTURE, not part of the aisotropy runtime build: it
// lives under testdata/ (which the go tool ignores for package builds, vet, and
// test) and is compiled on demand by the wasmtool test with
// `GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared`. It is the reference
// guest for the Aisotropy WASM tool ABI (see internal/wasmtool/wasmtool.go):
//
//   - It is a wasip1 *reactor* (buildmode c-shared): main() is never run; the
//     host calls the reactor initializer (_initialize) and then the exported
//     ABI functions below.
//   - aisotropy_alloc(size) -> ptr: the guest allocates `size` bytes of its OWN
//     linear memory and returns the offset. The host writes the JSON argument
//     bytes there. The guest pins every buffer (keepAlive) so the GC cannot
//     reclaim it before the host reads it back.
//   - aisotropy_run(ptr, len) -> packed i64: the guest reads the JSON args at
//     [ptr,ptr+len), computes a JSON result, and returns (resultPtr<<32)|len so
//     the host can read the result region out of guest memory.
//
// It also doubles as the ISOLATION PROOF: it *attempts* to read a host file and
// an env var and reports the outcome in its result. Under the wasmtool sandbox
// (no filesystem preopens, no environment, no network in wazero at all) the file
// read MUST fail and the env var MUST be empty — the test asserts exactly that.
package main

import (
	"encoding/json"
	"os"
	"unsafe"
)

// main is required for a main package but never executes in a reactor module.
func main() {}

// keepAlive pins buffers handed to the host. wazero reads guest memory
// synchronously right after an exported call returns, so retaining the buffers
// we allocated is enough to keep Go's non-moving wasm GC from reclaiming them
// mid-handoff. This fixture keeps all of them for simplicity.
var keepAlive [][]byte

//go:wasmexport aisotropy_alloc
func alloc(size uint32) uint32 {
	if size == 0 {
		size = 1
	}
	buf := make([]byte, size)
	keepAlive = append(keepAlive, buf)
	return ptrOf(buf)
}

//go:wasmexport aisotropy_run
func run(ptr, length uint32) uint64 {
	in := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), length) //nolint:govet // wasm linear-memory offset, stable under Go's non-moving wasm GC
	var args struct {
		Name string `json:"name"`
		N    int    `json:"n"`
	}
	_ = json.Unmarshal(in, &args)

	// Capability probes. With no preopened directories and no environment
	// configured on the host side, these MUST be denied — that denial, surfaced
	// in the result, is the sandbox-isolation proof the test asserts on.
	_, fsErr := os.ReadFile("/etc/hosts")

	out, _ := json.Marshal(map[string]any{
		"greeting":       "hello " + args.Name,
		"doubled":        args.N * 2,
		"fs_read_denied": fsErr != nil,
		"env_home":       os.Getenv("HOME"), // no env configured -> ""
	})
	keepAlive = append(keepAlive, out)
	return pack(ptrOf(out), uint32(len(out)))
}

// ptrOf returns the linear-memory offset of a byte slice's backing array.
func ptrOf(b []byte) uint32 {
	return uint32(uintptr(unsafe.Pointer(&b[0]))) //nolint:govet // see run()
}

// pack folds a (ptr,len) pair into the single i64 the ABI returns.
func pack(ptr, length uint32) uint64 { return (uint64(ptr) << 32) | uint64(length) }
