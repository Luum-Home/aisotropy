// Standalone module for the Phase-2 streaming transport bake-off.
// Deliberately separate from the root module so it adds ZERO dependencies to
// the aisotropy production go.mod. All deps below are MIT / Apache-2.0 / BSD /
// ISC (see README "License gate").
module aisotropy.local/bench/streaming

go 1.25.1

require (
	github.com/coder/websocket v1.8.15
	github.com/quic-go/quic-go v0.60.0
	github.com/quic-go/webtransport-go v0.11.1
	google.golang.org/grpc v1.82.1
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/dunglas/httpsfv v1.1.0 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	golang.org/x/crypto v0.51.0 // indirect
	golang.org/x/net v0.55.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260414002931-afd174a4e478 // indirect
)
