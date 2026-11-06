package desksand

import (
	"fmt"
	"os"
	"syscall"
)

// Main runs the wrapper's stages, `den desk-wrap net|walk ...`; it never
// returns. Den's binary runs it, and so does this package's real test,
// whose binary stands in for Den's.
//
//	net --host <prefix>... -- <bwrap argv>
//
// runs in pasta's namespace, closes the host's addresses and the private
// ranges with prohibit routes (all or nothing), then execs bwrap with only
// the sandbox's names in its environment;
//
//	walk --allow <path>...
//
// runs inside the sandbox and prints what it can reach that it should not.
func Main(args []string) {
	if len(args) == 0 {
		fail(fmt.Errorf("usage: den desk-wrap net|walk ..."))
	}
	switch args[0] {
	case "net":
		hosts, rest, err := NetArgs(args[1:])
		if err != nil {
			fail(err)
		}
		if err := Close(Routes(hosts)); err != nil {
			fail(err)
		}
		fail(syscall.Exec(rest[0], rest, PassEnv(os.Environ())))
	case "walk":
		var allow []string
		for i := 1; i < len(args); i++ {
			if args[i] != "--allow" || i+1 >= len(args) {
				fail(fmt.Errorf("unknown argument %q", args[i]))
			}
			allow = append(allow, args[i+1])
			i++
		}
		os.Stdout.WriteString(WalkOutput(SelfWalk(allow)))
		os.Exit(0)
	}
	fail(fmt.Errorf("usage: den desk-wrap net|walk ..."))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "den desk-wrap:", err)
	os.Exit(125)
}
