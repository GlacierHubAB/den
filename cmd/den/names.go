package main

import (
	"strings"
	"time"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/names"
	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/roster"
	"github.com/GlacierHubAB/den/internal/spawn"
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

// nameSpawned records the name Den gives a spawned agent, from its role
// and title, numbered so that it is free among every session Den can see
// and every one in the roster. Best effort: an agent Den could not name
// keeps its critter name.
func nameSpawned(res spawn.Result, in orch.SpawnInput) {
	if res.SessionID == "" || (strings.TrimSpace(in.Role) == "" && strings.TrimSpace(in.Title) == "") {
		return
	}
	names.Assign(names.RegistryPath(), res.SessionID, in.Role, in.Title, res.Critter, namespace(), time.Now())
}

// namespace is what a new name must not be: the peer handles and critter
// names of every session Den can see on this machine and of every one in
// the roster, which (with the live sessions) also hold their names.
func namespace() names.Namespace {
	taken, held := map[string]bool{}, map[string]bool{}
	add := func(ns ...string) {
		for _, n := range ns {
			if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
				taken[n] = true
				taken[names.Normalise(n)] = true
			}
		}
	}
	if live, err := discover.Scan(); err == nil {
		for _, s := range live {
			add(s.Name, badge.Name(s.SessionID))
			held[s.SessionID] = true
		}
	}
	for _, a := range roster.Load(roster.Path()).Agents {
		add(a.Peer, a.Critter)
		held[a.SessionID] = true
	}
	delete(taken, "")
	return names.Namespace{Taken: taken, Held: func(s string) bool { return held[s] }}
}
