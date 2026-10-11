# Demo: a team builds Den's website

The take: Den alone on screen, an empty project picked. You state one
mission. The orchestrator proposes a team with a model per role, you press
`^Y`, a product manager appears in its own terminal, staffs a designer, a
frontend developer, a copywriter and an SEO specialist in theirs, and the
Fleet tab draws the tree as it grows. The site lands in `~/code/den-site`.

## Once, before recording

1. Install and restart Den: `make install`, quit the running `den`, start
   it again. The demo needs v0.1.0-41 or later (`den --version`).
2. Trust the folder: run `claude` once in `~/code/den-site`, accept the
   trust dialog, `/exit`. Without this the first spawned terminal stops at
   that dialog and the orchestrator tells you so.
3. The project is pinned already (`~/.local/state/den/overview.json`). If
   the strip does not show `den-site 0`, type `/project add ~/code/den-site`.
4. Optional: a fresh orchestrator (`/orch new`) so the chat is empty, and
   `model = "opus"` under `[orchestrator]` in `~/.config/den/config.toml`
   for sharper briefs; `sonnet` works.
5. Spawned terminals open on the workspace you are looking at and tile
   beside Den. Record on a wide screen, or move Den to its own workspace
   after the spawns and cut between them.

## The take

1. Den on the Overview tab, `den-site` picked: "Nobody is working here
   right now", "No missions here yet".
2. Type the mission on the command line and press Enter:

   > Build a beautiful marketing website for Den, the app this fleet runs
   > in: design, copy and SEO, from the design and product files in the
   > repository. Staff it as a team.

   (Naming roles works too: "a product manager leading a UI designer, a
   frontend developer, a copywriter and an SEO specialist".)
3. The proposal: name, mission, done-when, constraints, shape `⇶ fan-out`,
   and `team` as a tree with a model per role. Refine by typing if you
   like ("use haiku for SEO"). `^Y`.
4. The run: the orchestrator spawns only the lead, on `opus`, in
   `~/code/den-site`; the spawn note becomes the lead's card. Its terminal
   opens. It reads `PRODUCT.md`, `DESIGN.md` and the README, then starts its
   people with its own spawn_agent tool, each on the model the brief named.
5. Fleet tab: `orchestrator` section, the lead with `directs 4`, four
   children under it with guides. `^T` for the map. `Enter` on the lead
   shows the chain of command and the `directs` list with each brief.
6. Inbox: permission prompts from any of the five, shown in full; `^Y`
   allows. On the Orchestrator tab the lead's card says `needs you · via
   <child>` when a child is waiting, and `^Y` there answers it.
7. Review tab: `den-site` picked, every agent's diff of the checkout.
8. Settling: when the lead's turn ends while its people still work, its
   card shows `waiting on 4 agents`; it finishes once they have reported
   and the lead reported up. Den asks the orchestrator whether the goal is
   met, and the mission goes `done` with a one-line summary.
9. The result: `cd ~/code/den-site && python3 -m http.server 8080`, open
   http://localhost:8080.

## Between takes

```sh
git -C ~/code/den-site reset -q --hard 5afcc25 && git -C ~/code/den-site clean -fdq
# with den closed: forget the demo missions, keep everything else
f=~/.local/state/den/missions.jsonl; grep -v '"project":"/home/grvlle/code/den-site"' "$f" > "$f.tmp" && mv "$f.tmp" "$f"
```

Close the agents' terminals (each is `den · <peer>` in its title). Edges
to gone sessions drop out of the tree on their own.

## What the pieces are

- `~/code/den-site/PRODUCT.md`: product truth, from Den's README.
- `~/code/den-site/DESIGN.md`: the visual world, derived from the design
  context GlacierHub uses for Aimless (dark monochrome, Geist, numbers in
  mono, squared edges, motion only for state), adapted to Den, with the
  landing page composition pinned so the designer does not open an
  interactive direction round.
- `~/code/den-site/CLAUDE.md`: working agreements for a shared checkout.
- `~/code/den-site/.claude/skills/impeccable`: the impeccable design skill
  (pbakaus/impeccable), which reads the two files above.
- Models: the spawn tool offers `haiku`, `sonnet` and `opus` with a line on
  when each fits; the orchestrator and the lead pick per task.
