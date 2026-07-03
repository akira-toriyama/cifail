package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/akira-toriyama/cifail/internal/core"
	"github.com/akira-toriyama/cifail/internal/model"
)

// JSON goes to stdout only; the error envelope goes to stderr. These writers are
// overridable in tests.
var (
	out    io.Writer = os.Stdout
	errOut io.Writer = os.Stderr
)

// renderResult writes the assembled result to stdout as pretty JSON, or compact
// single-line JSON with --ndjson.
func renderResult(r *model.Result) error {
	if flagNDJSON {
		printCompact(r)
		return nil
	}
	printPretty(r)
	return nil
}

// printPretty writes v as 2-space-indented JSON (HTML escaping off so log text
// with <, >, & stays readable).
func printPretty(v any) {
	fmt.Fprintln(out, string(marshal(v, true)))
}

// printCompact writes v as one JSON line.
func printCompact(v any) {
	fmt.Fprintln(out, string(marshal(v, false)))
}

func marshal(v any, indent bool) []byte {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if indent {
		e.SetIndent("", "  ")
	}
	_ = e.Encode(v)
	return bytes.TrimRight(b.Bytes(), "\n")
}

// renderError prints a structured error to stderr as {"error":{...}} so a caller
// piping stdout to jq is unaffected, and both agents and humans can read it.
func renderError(ce *core.Error) {
	type errBody struct {
		Code    int    `json:"code"`
		Msg     string `json:"message"`
		Details any    `json:"details,omitempty"`
	}
	env := struct {
		Error errBody `json:"error"`
	}{Error: errBody{Code: int(ce.Code), Msg: ce.Msg, Details: ce.Details}}
	fmt.Fprintln(errOut, string(marshal(env, true)))
}
