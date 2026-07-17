// Package keybrokering holds the key-brokering competitive arena. The actual
// head-to-head proof lives in broker_arena_test.go, which is compiled only under
// the `keybroker` build tag (it drives aisotropy.App.RunTask — the façade that is
// itself keybroker-tagged — against real Docker).
//
// This file carries no build tag on purpose: it gives the package one buildable
// Go source in the DEFAULT build so `go build ./...` / `go test ./...` see a
// valid (empty, test-free) package here instead of failing with "build
// constraints exclude all Go files". Run the arena with:
//
//	go test -tags keybroker ./arenas/key-brokering
package keybrokering
