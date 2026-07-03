// Command cifail extracts the failing logs from a GitHub Actions run in a
// bounded, high-signal form: it resolves a PR/branch/run to its latest failing
// workflow run, downloads the run log archive ONCE, keeps only the failed
// steps, and pares them to the error lines (plus context) that fit a byte
// budget — emitting JSON for an agent instead of the ~130 KB `gh run view
// --log-failed` dump.
//
// All logic lives in internal/{cli,gh,extract,model,core,version}; main only
// maps the CLI's resolved exit code to the process. See README.md.
package main

import (
	"os"

	"github.com/akira-toriyama/cifail/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
