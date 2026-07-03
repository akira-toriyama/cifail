module github.com/akira-toriyama/cifail

// Floor is a supported minor (never an EOL pin); `toolchain` names the build
// toolchain, and CI resolves it via `go-version-file: go.mod` (GOTOOLCHAIN=local).
go 1.25.0

toolchain go1.26.4

require github.com/spf13/cobra v1.10.2

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
)
