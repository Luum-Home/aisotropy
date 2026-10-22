module github.com/Luum-Home/aisotropy

go 1.26.0

// Build with a patched toolchain for the reachable standard-library CVEs.
// x/net v0.60.0 requires Go 1.26.0; local goenv pins select the patched compiler.
toolchain go1.26.10

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/anthropics/anthropic-sdk-go v1.61.0
	github.com/charmbracelet/bubbles v1.0.0
	github.com/charmbracelet/bubbletea v1.3.10
	github.com/charmbracelet/lipgloss v1.1.0
	github.com/charmbracelet/x/exp/teatest v0.0.0-20260705004817-2cc9a8fe1146
	github.com/danielgtaylor/huma/v2 v2.39.1
	github.com/dgraph-io/badger/v4 v4.9.5
	github.com/fugue-labs/gollem v0.8.0
	github.com/go-chi/chi/v5 v5.3.1
	github.com/goccy/go-yaml v1.19.2
	github.com/google/uuid v1.6.0
	github.com/invopop/jsonschema v0.14.0
	github.com/ledongthuc/pdf v0.0.0-20250511090121-5959a4027728
	github.com/pemistahl/lingua-go v1.4.0
	github.com/qdrant/go-client v1.18.3
	github.com/rqlite/gorqlite v0.0.0-20260504155303-50d445fd0ab9
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2
	go.opentelemetry.io/otel v1.45.0
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp v1.45.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.45.0
	go.opentelemetry.io/otel/sdk v1.45.0
	go.opentelemetry.io/otel/sdk/metric v1.45.0
	go.opentelemetry.io/otel/trace v1.45.0
	golang.org/x/tools v0.49.0
	modernc.org/sqlite v1.56.0
)

require github.com/shopspring/decimal v1.4.0 // indirect

require (
	github.com/aymanbagabas/go-osc52/v2 v2.0.1 // indirect
	github.com/aymanbagabas/go-udiff v0.3.1 // indirect
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.6.1 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/charmbracelet/colorprofile v0.4.3 // indirect
	github.com/charmbracelet/x/ansi v0.11.7 // indirect
	github.com/charmbracelet/x/cellbuf v0.0.15 // indirect
	github.com/charmbracelet/x/exp/golden v0.0.0-20241011142426-46044092ad91 // indirect
	github.com/charmbracelet/x/term v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/dgraph-io/ristretto/v2 v2.4.2 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/erikgeiser/coninput v0.0.0-20211004153227-1c3628e74d0f // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/flatbuffers v25.12.19+incompatible // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.29.0 // indirect
	github.com/klauspost/compress v1.19.1 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.1 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mattn/go-localereader v0.0.1 // indirect
	github.com/mattn/go-runewidth v0.0.27 // indirect
	github.com/muesli/ansi v0.0.0-20230316100256-276c6243b2f6 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/muesli/termenv v0.16.0 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/pb33f/ordered-map/v2 v2.3.1 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/standard-webhooks/standard-webhooks/libraries v0.0.1 // indirect
	github.com/tetratelabs/wazero v1.12.0
	github.com/tidwall/gjson v1.19.0 // indirect
	github.com/tidwall/match v1.2.0 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.45.0 // indirect
	go.opentelemetry.io/otel/metric v1.45.0
	go.opentelemetry.io/proto/otlp v1.11.0 // indirect
	go.yaml.in/yaml/v4 v4.0.0-rc.6 // indirect
	golang.org/x/exp v0.0.0-20260727155853-b88d891fe743 // indirect
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/net v0.60.0
	golang.org/x/sync v0.23.0
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0
	google.golang.org/genproto/googleapis/api v0.0.0-20260803160001-6ac0973c030d // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260803160001-6ac0973c030d // indirect
	google.golang.org/grpc v1.83.1
	google.golang.org/protobuf v1.36.11 // indirect
	modernc.org/libc v1.74.4 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)
