//go:build wasip1

// Command misbehavemod is a TEST FIXTURE (see echomod). Its run entry point
// misbehaves in a way selected by the JSON argument, so one module can exercise
// two distinct dispatch failure modes the host must contain:
//
//   - {"mode":"trap"}    -> the guest panics, which surfaces to wazero as a wasm
//     trap and to the host as a run-call error (exactly how untrusted code that
//     crashes is observed: a Go-side error, never an in-process crash).
//   - {"mode":"badread"} -> run returns a packed (ptr,len) result region that
//     points outside guest memory, so the host's bounds-checked Read of the
//     result refuses it rather than reading arbitrary memory.
//
// Any other input returns a normal, valid small result.
package main

import (
	"strings"
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
	in := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), length) //nolint:govet // wasm linear-memory offset
	s := string(in)
	switch {
	case strings.Contains(s, "trap"):
		panic("misbehave: intentional guest trap")
	case strings.Contains(s, "badread"):
		// A result region far outside guest memory: host Read must reject it.
		return pack(0xFFFF0000, 128)
	default:
		out := []byte(`{"ok":true}`)
		keepAlive = append(keepAlive, out)
		return pack(ptrOf(out), uint32(len(out)))
	}
}

func ptrOf(b []byte) uint32 {
	return uint32(uintptr(unsafe.Pointer(&b[0]))) //nolint:govet // wasm linear-memory offset
}

func pack(ptr, length uint32) uint64 { return (uint64(ptr) << 32) | uint64(length) }
