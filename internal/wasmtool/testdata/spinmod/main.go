//go:build wasip1

// Command spinmod is a TEST FIXTURE (see echomod). Its run entry point spins
// forever. wazero has NO CPU metering (no fuel, no epoch interruption), so the
// only thing that can stop this loop is the host's per-call DEADLINE: the
// runtime is built WithCloseOnContextDone, so when the dispatch context's
// timeout fires the guest call is torn down (checked at the loop back-edge), and
// the host observes a dispatch error at ~the deadline instead of a hang. This
// proves the CPU bound in internal/wasmtool/wasmtool.go is the deadline.
package main

import "unsafe"

func main() {}

// sink is a package-level side effect so the compiler cannot elide the loop.
var sink uint64

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
	for {
		sink++
	}
}

func ptrOf(b []byte) uint32 {
	return uint32(uintptr(unsafe.Pointer(&b[0]))) //nolint:govet // wasm linear-memory offset
}

func pack(ptr, length uint32) uint64 { return (uint64(ptr) << 32) | uint64(length) }
