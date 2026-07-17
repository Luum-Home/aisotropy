//go:build wasip1

// Command badallocmod is a TEST FIXTURE (see echomod). Its allocator returns a
// bogus, wildly-out-of-bounds offset instead of a real buffer, so when the host
// tries to WRITE the argument bytes at that offset the bounds check fails. It
// proves the dispatch path refuses to write past guest memory rather than
// corrupting it — a malformed/hostile guest allocator is contained.
package main

import "unsafe"

func main() {}

// alloc ignores the requested size and returns an offset near the top of the
// 32-bit address space, far beyond any real linear memory the module has.
//
//go:wasmexport aisotropy_alloc
func alloc(_ uint32) uint32 {
	return 0xFFFF0000
}

//go:wasmexport aisotropy_run
func run(_, _ uint32) uint64 {
	// Never reached: the host fails at mem.Write before calling run.
	out := []byte(`{"unreached":true}`)
	keepAlive = append(keepAlive, out)
	return pack(ptrOf(out), uint32(len(out)))
}

var keepAlive [][]byte

func ptrOf(b []byte) uint32 {
	return uint32(uintptr(unsafe.Pointer(&b[0]))) //nolint:govet // wasm linear-memory offset
}

func pack(ptr, length uint32) uint64 { return (uint64(ptr) << 32) | uint64(length) }
