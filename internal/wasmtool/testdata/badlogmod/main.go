//go:build wasip1

// Command badlogmod is a TEST FIXTURE (see echomod / logmod). It imports the
// same capability-gated aisotropy.log host function logmod does, but calls it
// with a (ptr,len) pointing WAY outside its own linear memory. It proves the
// host function's bounds check on the guest-supplied region is enforced: an
// out-of-bounds log call is silently dropped (see instantiateHostModule) rather
// than the host reading arbitrary process memory.
package main

import "unsafe"

func main() {}

//go:wasmimport aisotropy log
func hostLog(ptr, length uint32)

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
func run(_, _ uint32) uint64 {
	// Wildly out-of-bounds: far beyond any real linear memory this module has.
	hostLog(0xFFFF0000, 128)

	out := []byte(`{"logged":true}`)
	keepAlive = append(keepAlive, out)
	return pack(ptrOf(out), uint32(len(out)))
}

func ptrOf(b []byte) uint32 {
	return uint32(uintptr(unsafe.Pointer(&b[0]))) //nolint:govet // wasm linear-memory offset
}

func pack(ptr, length uint32) uint64 { return (uint64(ptr) << 32) | uint64(length) }
