//go:build wasip1

// Command badallocresultmod is a TEST FIXTURE (see echomod). Its allocator
// exports zero results instead of the one (ptr) the ABI requires, so the host's
// dispatch-time result-count check on the aisotropy_alloc call must reject it —
// proving a guest that doesn't even honor the ABI's return arity is contained,
// not just one that returns a bad-but-present value (see badallocmod).
package main

func main() {}

//go:wasmexport aisotropy_alloc
func alloc(_ uint32) {
	// Intentionally returns nothing: the ABI requires exactly one (ptr) result.
}

//go:wasmexport aisotropy_run
func run(_, _ uint32) uint64 {
	// Never reached: the host fails the alloc result-count check first.
	return 0
}
