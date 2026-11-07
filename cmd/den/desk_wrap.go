package main

import "github.com/GlacierHubAB/den/internal/desksand"

// deskWrap is the sandbox's own stages (desksand.Main), run by Den with
// fixed shapes, never with an agent's words.
func deskWrap(args []string) { desksand.Main(args) }
