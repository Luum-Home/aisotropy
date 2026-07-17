//go:build wasip1

// Command hogmod is a TEST FIXTURE (see echomod). Its run entry point tries to
// grow the guest's linear memory far past any sane cap by retaining a stream of
// 1 MiB chunks. Under the host's WithMemoryLimitPages the underlying memory.grow
// eventually fails, Go's runtime throws "out of memory", and the guest TRAPS —
// so the host observes a dispatch error, never an unbounded host allocation.
// This proves the hard memory bound in internal/wasmtool/wasmtool.go.
package main

import "unsafe"

func main() {}

// keepAlive retains every chunk so the allocator cannot free it and the guest
// heap must actually grow toward the page limit.
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
	// Grow up to ~4 GiB in 1 MiB steps. Under any WithMemoryLimitPages cap below
	// that the grow fails long before the loop ends and the guest traps. Touching
	// each chunk forces it to materialize (not be optimized away).
	for i := 0; i < 4096; i++ {
		chunk := make([]byte, 1<<20)
		chunk[0] = byte(i)
		keepAlive = append(keepAlive, chunk)
	}
	// Only reachable if the cap somehow did not bind — the test asserts it does.
	out := []byte(`{"unbounded":true}`)
	keepAlive = append(keepAlive, out)
	return pack(ptrOf(out), uint32(len(out)))
}

func ptrOf(b []byte) uint32 {
	return uint32(uintptr(unsafe.Pointer(&b[0]))) //nolint:govet // wasm linear-memory offset
}

func pack(ptr, length uint32) uint64 { return (uint64(ptr) << 32) | uint64(length) }
