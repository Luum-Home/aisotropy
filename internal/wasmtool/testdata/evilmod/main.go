//go:build wasip1

// Command evilmod is a TEST FIXTURE (see echomod for why testdata guests are not
// part of the runtime build). It imports a host function from an UNGRANTED host
// module ("env"), which the sandbox exposes no capability for. The capability
// gate must refuse to load it at New time regardless of WASI being granted: the
// guest is trying to reach authority the deployer never handed it.
package main

import "unsafe"

func main() {}

// evil is imported from the "env" module — NOT wasi_snapshot_preview1 and NOT
// aisotropy. The wasmtool sandbox grants no such module, so verifyImports rejects
// the whole module at load.
//
//go:wasmimport env evil
func evil()

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
	evil() // never reached: the module cannot instantiate without the "env" import
	out := []byte(`{"ok":true}`)
	keepAlive = append(keepAlive, out)
	return pack(ptrOf(out), uint32(len(out)))
}

func ptrOf(b []byte) uint32 {
	return uint32(uintptr(unsafe.Pointer(&b[0]))) //nolint:govet // wasm linear-memory offset
}

func pack(ptr, length uint32) uint64 { return (uint64(ptr) << 32) | uint64(length) }
