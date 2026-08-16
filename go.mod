module github.com/akira-toriyama/cifail

// Floor is a supported minor (never an EOL pin); `toolchain` names the build
// toolchain. CI leaves GOTOOLCHAIN unset so setup-go reads this file, honors the
// `toolchain` line and pins it; a job-level GOTOOLCHAIN=local up front would make
// setup-go skip the toolchain line and install the bare floor instead.
go 1.25.0

toolchain go1.26.6

require github.com/spf13/cobra v1.10.2

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
)
