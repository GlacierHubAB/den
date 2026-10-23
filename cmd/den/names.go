package main

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/names"
	"github.com/GlacierHubAB/den/internal/orch"
)

// needsTitle says what spawn_agent must be given before it starts an agent
// with a role too vague to name it by ("coding agent"), "" when nothing.
// A spawn with no role at all keeps the critter name.
func needsTitle(in orch.SpawnInput) string {
	if strings.TrimSpace(in.Role) == "" || !names.Generic(in.Role) || names.Normalise(in.Title) != "" {
		return ""
	}
	return "role " + strings.TrimSpace(in.Role) + " says nothing about the work: give title, a few words on this agent's job (\"release notes writer\", \"payments backend engineer\"), which Den names it by"
}

// askSpawned reaches Den; tests replace it.
var askSpawned = bridge.Call

// reportSpawn tells Den this den mcp started an agent with peer handle
// peer: Den names a lead's spawn only when its own den mcp says so too,
// not on the strength of a hook alone. Best effort.
func reportSpawn(peer string) {
	if peer == "" {
		return
	}
	payload, _ := json.Marshal(bridge.SpawnedInput{Peer: peer})
	askSpawned(bridge.SocketPath(), bridge.Envelope{Seq: time.Now().UnixNano(), Event: bridge.SpawnedEvent, Payload: payload}, 2*time.Second)
}
