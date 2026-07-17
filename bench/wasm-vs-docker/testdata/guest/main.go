//go:build wasip1

// Command guest is the constant-work WASM fixture for the wasm-vs-docker
// benchmark: a trivial pure-compute JSON->JSON "echo" tool matching the aisotropy
// WASM tool ABI (aisotropy_alloc / aisotropy_run — see internal/wasmtool). It
// lives under testdata/ so the go tool ignores it for normal package builds/vet
// (it only compiles under GOOS=wasip1 GOARCH=wasm, which the harness does at run
// time). The work it does is deliberately minimal — parse {"name","n"}, return a
// tiny JSON object — so the benchmark measures the ISOLATION MECHANISM (wazero
// instantiate + dispatch) held against the container path doing the same trivial
// echo, not the tool's own compute.
package main

import (
	"encoding/json"
	"unsafe"
)

func main() {}

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
	out, _ := json.Marshal(map[string]any{
		"greeting": "hello " + args.Name,
		"doubled":  args.N * 2,
	})
	keepAlive = append(keepAlive, out)
	return pack(ptrOf(out), uint32(len(out)))
}

func ptrOf(b []byte) uint32 {
	return uint32(uintptr(unsafe.Pointer(&b[0]))) //nolint:govet // see run()
}

func pack(ptr, length uint32) uint64 { return (uint64(ptr) << 32) | uint64(length) }
