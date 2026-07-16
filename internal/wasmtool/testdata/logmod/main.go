//go:build wasip1

// Command logmod is a TEST FIXTURE (see echomod for why testdata guests are not
// part of the runtime build). It exercises the ONE optional, capability-gated
// host function the wasmtool sandbox can expose: `aisotropy.log`. The guest
// declares it with //go:wasmimport, which means the module IMPORTS the host
// function `log` from module `aisotropy` — so this guest can only instantiate
// when the runner was built with the Log capability enabled. Running it under a
// runner with Log disabled fails at instantiation, which is precisely the
// capability gate: a guest cannot obtain a host function the deployer did not
// grant.
package main

import "unsafe"

func main() {}

// hostLog is provided by the runner ONLY when the Log capability is enabled. The
// host reads the message bytes at [ptr,ptr+len) out of guest memory and forwards
// them to the deployer's callback. It is the sole channel out of the sandbox.
//
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
	msg := []byte("log-from-guest")
	keepAlive = append(keepAlive, msg)
	hostLog(ptrOf(msg), uint32(len(msg)))

	out := []byte(`{"logged":true}`)
	keepAlive = append(keepAlive, out)
	return pack(ptrOf(out), uint32(len(out)))
}

func ptrOf(b []byte) uint32 {
	return uint32(uintptr(unsafe.Pointer(&b[0]))) //nolint:govet // wasm linear-memory offset, stable under Go's non-moving wasm GC
}

func pack(ptr, length uint32) uint64 { return (uint64(ptr) << 32) | uint64(length) }
