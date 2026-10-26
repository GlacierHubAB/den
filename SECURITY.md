# Security

Den sits between your coding agents and the decisions that are yours: it shows
their permission requests, carries your answers back, starts and closes
agents, and reads text that agents, repositories and other machines wrote. The
parts we care most about are the approvals, the fencing of untrusted text, and
the checks Den makes before it acts.

## Reporting a vulnerability

Please do not open a public issue. Report it privately through GitHub: on
this repository's Security tab, choose "Report a vulnerability". Only the
maintainers and you can see the report.

We aim to acknowledge a report within 3 working days and to fix it, or publish
a mitigation, within 90 days of the report. We will credit you in the release
notes unless you ask us not to.

## What counts

Anything that lets an agent, a transcript, a hook payload, a repository, a
skill, a remote host or a paired app do more than the design allows:

- approve, allow, deny or answer a request, or start, close or message an
  agent, on your behalf without your `Ctrl+Y`, beyond what you turned on
  yourself (a `/always` answer, God mode, a paired console's powers);
- get text taken as your words: a peer message past the orchestrator's inbound
  hook, a message to a God-mode agent past Den's mediation, or an agent's
  report, note or file treated as an instruction or a rule;
- make a permission mode, a remote host's policy or a console's powers ask
  less without you;
- make Den run a program through a repository's git config, a skill, a
  template, a file name or the text it shows (escape sequences included);
- install or change a skill, or the desktop tool, other than at its pinned
  commit and hashes;
- read a secret Den holds, or show one to a model, a remote host or a paired
  app;
- let a desktop agent act outside its gate: on Den's own windows, outside an
  app mission's apps, or past a task's limits;
- let a remote host act on this machine beyond its policy.

Design-level attacks on these rules are in scope too.

## What does not count

- What the README says stays open by design: anything running as your user
  can change Den's files, type into its window or drive a paired app, and an
  agent in God mode, or in a mode a classifier approves, can do what you can.
  See "God mode", "Apps (den-api.sock)" and "Desktop control" in the
  [README](README.md).
- Problems in Claude Code itself: please report those to Anthropic. The same
  goes for the upstream projects Den pins (hypruse, the catalogue's skills):
  report to their authors, and tell us if Den's pin should move.

## Supported versions

Only the latest release. Den is pre-1.0; there is no backport policy yet.

## Practices

- Every GitHub Action is pinned by commit SHA, and every workflow starts from
  no permissions, granting each job only what it needs.
- Secret scanning (gitleaks over the whole history) and Go vulnerability
  scanning (govulncheck) run on every pull request, on every push to `dev` and
  `main`, and weekly.
- The race detector runs three times on two CPUs for every pull request.
  Adversarial tests cover remote hosts, hostile git configuration and untrusted
  text. The remote-host protocol, the systemd message reader and the text
  sanitiser have fuzz targets.
- Releases are built by CI from a tag on `main`, with a build provenance
  attestation and published checksums.
- The skills catalogue and the desktop tool are pinned by commit and hashed,
  file by file.

How Den enforces all this while it runs is in the README's
[Security and quality model](README.md#security-and-quality-model).
