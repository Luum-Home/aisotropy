//go:build wasip1

// Command badrunresultmod is a TEST FIXTURE (see echomod). Its allocator is
// well-behaved (returns a real buffer), but its run entry point exports zero
// results instead of the one packed (ptr<<32|len) i64 the ABI requires. This
// proves the host's dispatch-time result-count check on the aisotropy_run call
// rejects a guest that doesn't honor the ABI's return arity, distinct from
// misbehavemod's already-covered "traps" and "bad result region" cases.
package main

import "unsafe"

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
func run(_, _ uint32) {
	// Intentionally returns nothing: the ABI requires exactly one packed result.
}

func ptrOf(b []byte) uint32 {
	return uint32(uintptr(unsafe.Pointer(&b[0]))) //nolint:govet // wasm linear-memory offset
}
