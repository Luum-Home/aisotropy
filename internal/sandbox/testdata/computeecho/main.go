//go:build wasip1

// Command computeecho is a TEST FIXTURE (under testdata/, ignored by the go tool
// for package builds, vet, and test), compiled on demand to a wasip1 reactor by
// the WASMProvider test. It is a trivial pure-compute JSON->JSON tool matching
// the aisotropy WASM tool ABI (see internal/wasmtool/wasmtool.go):
//
//   - aisotropy_alloc(size) -> ptr: allocate guest memory for the JSON args.
//   - aisotropy_run(ptr, len) -> packed i64 = (resultPtr<<32)|resultLen.
//
// It reads {"name":..., "n":...} and returns a small JSON object, doing no I/O —
// exactly the pure-compute subset the WASM compute provider serves. It also
// probes a host file read to confirm the sandbox denies it (the same isolation
// proof the plugin sandbox fixture makes).
package main

import (
	"encoding/json"
	"os"
	"unsafe"
)

func main() {}

// keepAlive pins buffers handed to the host until wazero reads them back.
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

	_, fsErr := os.ReadFile("/etc/hosts")

	out, _ := json.Marshal(map[string]any{
		"greeting":       "hello " + args.Name,
		"doubled":        args.N * 2,
		"fs_read_denied": fsErr != nil,
	})
	keepAlive = append(keepAlive, out)
	return pack(ptrOf(out), uint32(len(out)))
}

func ptrOf(b []byte) uint32 {
	return uint32(uintptr(unsafe.Pointer(&b[0]))) //nolint:govet // see run()
}

func pack(ptr, length uint32) uint64 { return (uint64(ptr) << 32) | uint64(length) }
