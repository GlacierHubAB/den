package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/GlacierHubAB/den/internal/desktop"
)

// desktopView is `den desktop-view --exec -- <claude argv…>`, what a
// desktop agent's terminal runs. It starts the agent itself (an argv, no
// shell between Den and the prompt), shows its stream-json as the
// agent's words and tool calls, sanitised (desktop.View), forwards Ctrl+C
// and the terminal's hang-up to it, and says how it ended. Then it waits
// for Enter (ten minutes at most) so the human can read the end before
// the window closes. It exits with the agent's status.
func desktopView(args []string) {
	if len(args) < 3 || args[0] != "--exec" || args[1] != "--" {
		fail("usage: den desktop-view --exec -- <command…>")
	}
	argv := args[2:]
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		fail("den desktop-view: %v", err)
	}
	if err := cmd.Start(); err != nil {
		fail("den desktop-view: start the agent: %v", err)
	}
	// Ctrl+C, a closed window or a stop reach the agent too; its desktop
	// server and hypruse end with it (they are its children, and hypruse
	// has a parent-death signal).
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for s := range sigs {
			cmd.Process.Signal(s)
		}
	}()
	desktop.View(out, os.Stdout)
	err = cmd.Wait()
	signal.Stop(sigs)
	status := 0
	var exit *exec.ExitError
	switch {
	case err == nil:
		fmt.Println("\nThe desktop agent has ended.")
	case errors.As(err, &exit) && exit.ExitCode() >= 0:
		status = exit.ExitCode()
		fmt.Printf("\nThe desktop agent ended with status %d.\n", status)
	case errors.As(err, &exit):
		status = 1
		fmt.Printf("\nThe desktop agent was stopped (%v).\n", exit.ProcessState)
	default:
		status = 1
		fmt.Printf("\nThe desktop agent failed: %v\n", err)
	}
	fmt.Println("Press Enter to close this window.")
	done := make(chan struct{})
	go func() {
		if tty, err := os.Open("/dev/tty"); err == nil {
			buf := make([]byte, 1)
			tty.Read(buf)
			tty.Close()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Minute):
	}
	os.Exit(status)
}
