module github.com/belluikit/alidade

go 1.26.2

require (
	github.com/evanw/esbuild v0.28.2
	github.com/jinzhu/inflection v1.0.0
	github.com/mark3labs/mcp-go v1.2.1
	github.com/microsoft/typescript-go/shim v0.0.0
	github.com/sqlc-dev/oliphant v0.2.0
	github.com/tommie/v8go v0.37.0
	github.com/trendvidia/caliper v1.7.0
	github.com/trendvidia/chameleon v0.25.2
	github.com/trendvidia/goasm v1.1.1
	github.com/trendvidia/protocompile v0.34.0
	github.com/trendvidia/protowire-go v1.8.1
	github.com/trendvidia/pylib v0.1.0
	github.com/yuin/goldmark v1.8.6
	golang.org/x/mod v0.42.0
	golang.org/x/tools v0.52.0
	google.golang.org/protobuf v1.36.12
	gopkg.in/yaml.v3 v3.0.1
)

require (
	buf.build/gen/go/bufbuild/protodescriptor/protocolbuffers/go v1.36.12-20250109164928-1da0de137947.1 // indirect
	buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.12-20240920164238-5a7b106cbb87.1 // indirect
	github.com/fzipp/gocyclo v0.6.0 // indirect
	github.com/go-json-experiment/json v0.0.0-20260623181947-01eb4420fa68 // indirect
	github.com/google/jsonschema-go v0.4.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/klauspost/cpuid/v2 v2.2.10 // indirect
	github.com/microsoft/typescript-go v0.0.0 // indirect
	github.com/petermattis/goid v0.0.0-20260113132338-7c7de50cc741 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	github.com/spf13/cast v1.7.1 // indirect
	github.com/tidwall/btree v1.8.1 // indirect
	github.com/tommie/v8go/deps/android_amd64 v0.0.0-20261002084705-a57e453c0be5 // indirect
	github.com/tommie/v8go/deps/android_arm64 v0.0.0-20261002084705-a57e453c0be5 // indirect
	github.com/tommie/v8go/deps/darwin_amd64 v0.0.0-20261002084705-a57e453c0be5 // indirect
	github.com/tommie/v8go/deps/darwin_arm64 v0.0.0-20261002084705-a57e453c0be5 // indirect
	github.com/tommie/v8go/deps/linux_amd64 v0.0.0-20261002084705-a57e453c0be5 // indirect
	github.com/tommie/v8go/deps/linux_arm64 v0.0.0-20261002084705-a57e453c0be5 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	github.com/zeebo/xxh3 v1.1.0 // indirect
	golang.org/x/exp v0.0.0-20250911091902-df9299821621 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260807164820-c8921c73eeea // indirect
)

tool github.com/fzipp/gocyclo/cmd/gocyclo

// typescript-go has no public Go API (ADR 007, amended for #215): the
// shim reaches its internal packages, and both come from the tree.
replace (
	github.com/microsoft/typescript-go => ./third_party/typescript-go
	github.com/microsoft/typescript-go/shim => ./third_party/tsgo-shim
)
