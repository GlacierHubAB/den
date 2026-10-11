package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/skills"
	"github.com/GlacierHubAB/den/internal/spawn"
)

// skillsWait is how long a skills offer waits for the human.
const skillsWait = 10 * time.Minute

// asker names who is proposing: the lead's peer name, or the orchestrator.
func asker(lead string) string {
	if lead != "" {
		return lead
	}
	return "orchestrator"
}

// askSkills puts the offer in Den's inbox and waits for the human.
func askSkills(in bridge.SkillsInput) (bridge.Decision, bool) {
	payload, _ := json.Marshal(in)
	env := bridge.Envelope{Seq: time.Now().UnixNano(), Event: bridge.SkillsEvent, Payload: payload}
	return bridge.Ask(bridge.SocketPath(), env, skillsWait)
}

func names(ms []skills.Match) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Name)
	}
	return out
}

// offerAgentSkills asks the human for the role's skills before an agent
// starts; on approval Den has installed them into a folder of their own
// and the agent is started with it. The agent starts either way.
func offerAgentSkills(req *spawn.Request, role string, want []string, by string) (string, error) {
	cat, err := skills.Load()
	if err != nil {
		return "", err
	}
	ms, err := cat.Pick(role, want)
	if err != nil {
		return "", err
	}
	if len(ms) == 0 {
		return fmt.Sprintf("No catalogue skill fits the role %q, so none were offered.", role), nil
	}
	dir, err := spawn.ExpandDir(req.Dir)
	if err != nil {
		return "", err
	}
	// Fix the peer name now: the skills folder is named for it.
	sessions, _ := discover.Scan()
	req.Name = spawn.PeerName(req.Name, dir, sessions)
	root := skills.AgentRoot(req.Name, time.Now())
	d, ok := askSkills(bridge.SkillsInput{Role: role, Skills: names(ms), Scope: string(skills.Agent), Dir: dir, Peer: req.Name, Root: root, AskedBy: by})
	switch {
	case !ok:
		return "Den did not answer the skills offer, so the agent started without skills.", nil
	case d.Behavior == "allow":
		req.AddDirs = append(req.AddDirs, root)
		return "Skills, approved by the human for this agent only: " + d.Message + ".", nil
	default:
		return "No skills were installed, so the agent started without them: " + d.Message + ".", nil
	}
}

// offerProjectSkills asks the human to install the role's skills into a
// project.
func offerProjectSkills(in orch.SkillsInput, by string) (string, error) {
	cat, err := skills.Load()
	if err != nil {
		return "", err
	}
	ms, err := cat.Pick(in.Role, in.Skills)
	if err != nil {
		return "", err
	}
	if len(ms) == 0 {
		return fmt.Sprintf("No catalogue skill fits the role %q; nothing was offered.", in.Role), nil
	}
	dir, err := spawn.ExpandDir(in.Dir)
	if err != nil {
		return "", err
	}
	d, ok := askSkills(bridge.SkillsInput{Role: in.Role, Skills: names(ms), Scope: string(skills.Project), Dir: dir, AskedBy: by, Reason: in.Reason})
	switch {
	case !ok:
		return "", errorString("Den is not running or did not answer; nothing was installed")
	case d.Behavior == "allow":
		return "Approved by the human: " + d.Message + ". Sessions in that project find them in .claude/skills; one that started before the folder existed needs /reload-skills.", nil
	default:
		return "No skills were installed: " + d.Message + ".", nil
	}
}

// skillsCmd is `den skills`: the catalogue, a role's matches, what Den
// installed, and removing it again.
func skillsCmd(args []string) {
	sub := "list"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "catalogue", "catalog":
		cat, err := skills.Load()
		if err != nil {
			fail("%v", err)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "SKILL\tROLES\tREPO\tCOMMIT\tLICENSE\tWHY")
		for _, s := range cat.Skills {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", s.Name, strings.Join(s.Roles, ","), s.Repo, s.Commit[:12], s.License, s.Reason)
		}
		tw.Flush()
	case "match":
		if len(args) == 0 {
			fail("usage: den skills match <role>")
		}
		cat, err := skills.Load()
		if err != nil {
			fail("%v", err)
		}
		role := strings.Join(args, " ")
		ms := cat.Match(role)
		if len(ms) == 0 {
			fmt.Printf("No catalogue skill fits %q (tags: %s).\n", role, strings.Join(skills.RoleTags(role), ", "))
			return
		}
		for _, m := range ms {
			fmt.Printf("%s · %s @ %s · %s\n  %s\n", m.Name, m.Repo, m.Commit, m.License, m.Reason)
		}
	case "list", "ls":
		p, err := skills.LoadProvenance(skills.ProvenancePath())
		if err != nil {
			fail("%v", err)
		}
		if len(p.Installed) == 0 {
			fmt.Println("Den has installed no skills.")
			return
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "SKILL\tFOR\tREPO\tCOMMIT\tLICENSE\tINSTALLED\tAPPROVED BY\tASKED BY\tPATH")
		for _, r := range p.Installed {
			who := "project " + r.Dir
			if r.Scope == skills.Agent {
				who = "agent " + r.Peer
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Name, who, r.Repo, r.Commit[:12], r.License, r.InstalledAt.Local().Format("2006-01-02 15:04"), r.ApprovedBy, r.AskedBy, r.Path)
		}
		tw.Flush()
	case "remove", "rm":
		if len(args) == 0 {
			fail("usage: den skills remove <skill> [<project folder | agent peer name | install path>]")
		}
		where := ""
		if len(args) > 1 {
			where = args[1]
			if d, err := spawn.ExpandDir(where); err == nil {
				where = d
			}
		}
		hits, kept, err := skills.Uninstall(skills.ProvenancePath(), args[0], where)
		if err != nil {
			fail("%v", err)
		}
		for _, r := range hits {
			fmt.Println("Removed " + r.Name + " from " + r.Path)
		}
		for _, k := range kept {
			fmt.Println("Kept " + k + ": it changed after Den installed it")
		}
	default:
		fail("usage: den skills [list | catalogue | match <role> | remove <skill> [where]]")
	}
}
