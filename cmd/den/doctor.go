package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/GlacierHubAB/den/internal/doctor"
)

// doctorCmd is `den doctor [--json]`: read-only checks of Den's setup, safe
// to paste into an issue. It exits 1 when a check fails.
func doctorCmd(args []string) {
	os.Exit(runDoctor(doctor.System(), args, os.Stdout))
}

// runDoctor prints the checks of e to w and returns the exit code.
func runDoctor(e doctor.Env, args []string, w io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(w)
	asJSON := fs.Bool("json", false, "print the results as JSON: [{id, status, summary, fix?}]")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rs := doctor.Run(e)
	if *asJSON {
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false) // <CLAUDE_CONFIG_DIR> reads as written
		enc.SetIndent("", "  ")
		enc.Encode(rs)
	} else {
		fmt.Fprint(w, doctor.Text(rs))
	}
	if doctor.Failed(rs) {
		return 1
	}
	return 0
}
