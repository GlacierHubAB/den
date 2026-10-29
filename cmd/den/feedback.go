package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/GlacierHubAB/den/internal/doctor"
)

// feedbackCmd is `den feedback [--days N] [--json]`: a report to paste into
// a bug report, for the human to read first. It writes nothing, copies
// nothing and sends nothing.
func feedbackCmd(args []string) {
	e := doctor.System()
	e.DenVersion = version
	st, err := os.Stdout.Stat()
	tty := err == nil && st.Mode()&os.ModeCharDevice != 0
	os.Exit(runFeedback(e, args, os.Stdout, os.Stderr, tty))
}

// runFeedback prints the report of e to out and the notice to errw, and
// again after the report when out is a terminal. The notice never goes to
// out, so a file or a pipe holds only the report.
func runFeedback(e doctor.Env, args []string, out, errw io.Writer, tty bool) int {
	fs := flag.NewFlagSet("feedback", flag.ContinueOnError)
	fs.SetOutput(errw)
	days := fs.Int("days", 7, fmt.Sprintf("the last N days, 1 to %d", doctor.MaxDays))
	asJSON := fs.Bool("json", false, "print the report as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *days < 1 || *days > doctor.MaxDays {
		fmt.Fprintf(errw, "den: --days is 1 to %d\n", doctor.MaxDays)
		return 2
	}
	fmt.Fprint(errw, doctor.Notice)
	r := doctor.Feedback(e, *days)
	if *asJSON {
		raw, err := r.JSON(e.Names())
		if err != nil {
			fmt.Fprintln(errw, "den: the report does not encode")
			return 1
		}
		out.Write(raw)
	} else {
		fmt.Fprint(out, r.Text(e.Names()))
	}
	if tty {
		fmt.Fprint(errw, "\n"+doctor.Notice)
	}
	return 0
}
