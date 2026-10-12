# GitHub setup for the branch workflow

The repository settings behind [RELEASING.md](RELEASING.md): the `dev` branch,
rulesets for `dev`, `main` and `v*` tags, merge settings, Actions settings and
Dependabot. They are GitHub settings, not files, so they are written here as
commands for a maintainer with admin rights on `GlacierHubAB/den` to run, in
order. Each step says how to check it.

Set this once in the shell you run them from:

```
R=GlacierHubAB/den
```

## 0. Before you start

The workflows in `.github/` (with the `ci-passed` job) must be on `main`
first: the rulesets below require a check that only exists once that `ci.yml`
runs, and Dependabot reads its configuration from the default branch. Land
them on `main` the usual way for this one change, then continue here.

Check:

```
gh api "repos/$R/contents/.github/workflows/ci.yml?ref=main" --jq .name
gh api "repos/$R/contents/.github/dependabot.yml?ref=main" --jq .name
gh run list --repo "$R" --workflow ci --branch main --limit 1   # completed, success
```

## 1. Create `dev` from `main`

```
gh api -X POST "repos/$R/git/refs" \
  -f ref=refs/heads/dev \
  -f sha="$(gh api "repos/$R/git/ref/heads/main" --jq .object.sha)"
```

Check: `gh api "repos/$R/branches/dev" --jq .commit.sha` prints the same SHA as `main`.

`main` stays the default branch, so the README visitors see describes the
released Den. Pull requests from forks will default to `main`; the PR template
and CONTRIBUTING.md ask for `dev`, and a maintainer can change a pull
request's base in the web page.

## 2. Merge settings

Squash merges for `dev` and merge commits for `main` (the rulesets below pick
which one each branch accepts); no rebase merges, because GitHub rewrites every
commit of a rebase merge and `dev` would stop being an ancestor of `main`.
Merged branches are deleted.

```
gh api -X PATCH "repos/$R" \
  -F allow_squash_merge=true \
  -F allow_merge_commit=true \
  -F allow_rebase_merge=false \
  -F allow_auto_merge=false \
  -F delete_branch_on_merge=true \
  -F allow_update_branch=false \
  -f squash_merge_commit_title=PR_TITLE \
  -f squash_merge_commit_message=COMMIT_MESSAGES \
  -f merge_commit_title=PR_TITLE \
  -f merge_commit_message=PR_BODY
```

With `COMMIT_MESSAGES` the squashed commit's body lists the branch's commit
messages. Read it in the merge dialog before you confirm, and delete anything
that should not be in the public history (trailers, private paths).

Check:

```
gh api "repos/$R" --jq '{allow_squash_merge,allow_merge_commit,allow_rebase_merge,delete_branch_on_merge}'
```

## 3. Ruleset for `dev`

Pull requests only, squash only, `ci-passed` must pass, linear history, no
force-push, no deletion. `integration_id` 15368 is the GitHub Actions app, so
only a check made by Actions counts. Approvals are 0 while Den has one
maintainer (GitHub does not let you approve your own pull request); raise
`required_approving_review_count` to 1 when there is a second.

```
gh api -X POST "repos/$R/rulesets" --input - <<'JSON'
{
  "name": "dev",
  "target": "branch",
  "enforcement": "active",
  "conditions": { "ref_name": { "include": ["refs/heads/dev"], "exclude": [] } },
  "bypass_actors": [],
  "rules": [
    { "type": "deletion" },
    { "type": "non_fast_forward" },
    { "type": "required_linear_history" },
    { "type": "pull_request", "parameters": {
        "required_approving_review_count": 0,
        "dismiss_stale_reviews_on_push": true,
        "require_code_owner_review": false,
        "require_last_push_approval": false,
        "required_review_thread_resolution": true,
        "allowed_merge_methods": ["squash"] } },
    { "type": "required_status_checks", "parameters": {
        "strict_required_status_checks_policy": false,
        "do_not_enforce_on_create": true,
        "required_status_checks": [ { "context": "ci-passed", "integration_id": 15368 } ] } }
  ]
}
JSON
```

## 4. Ruleset for `main`

The same, except merge commits only and no linear-history rule (release merges
are merge commits). "Require branches to be up to date" stays off: after a
hotfix, `dev` does not contain `main`'s merge commit, and updating `dev` from
`main` would need a merge commit on `dev`, which its ruleset refuses.

```
gh api -X POST "repos/$R/rulesets" --input - <<'JSON'
{
  "name": "main",
  "target": "branch",
  "enforcement": "active",
  "conditions": { "ref_name": { "include": ["~DEFAULT_BRANCH"], "exclude": [] } },
  "bypass_actors": [],
  "rules": [
    { "type": "deletion" },
    { "type": "non_fast_forward" },
    { "type": "pull_request", "parameters": {
        "required_approving_review_count": 0,
        "dismiss_stale_reviews_on_push": true,
        "require_code_owner_review": false,
        "require_last_push_approval": false,
        "required_review_thread_resolution": true,
        "allowed_merge_methods": ["merge"] } },
    { "type": "required_status_checks", "parameters": {
        "strict_required_status_checks_policy": false,
        "do_not_enforce_on_create": false,
        "required_status_checks": [ { "context": "ci-passed", "integration_id": 15368 } ] } }
  ]
}
JSON
```

## 5. Rulesets for `v*` tags

Two rulesets, so that being allowed to create a tag does not also allow moving
or deleting one. The first has no bypass at all: no one, admins included, can
move or delete a `v*` tag. The second lets only repository admins
(`RepositoryRole` 5) create one.

```
gh api -X POST "repos/$R/rulesets" --input - <<'JSON'
{
  "name": "release tags are immutable",
  "target": "tag",
  "enforcement": "active",
  "conditions": { "ref_name": { "include": ["refs/tags/v*"], "exclude": [] } },
  "bypass_actors": [],
  "rules": [ { "type": "deletion" }, { "type": "update" }, { "type": "non_fast_forward" } ]
}
JSON

gh api -X POST "repos/$R/rulesets" --input - <<'JSON'
{
  "name": "release tags by admins only",
  "target": "tag",
  "enforcement": "active",
  "conditions": { "ref_name": { "include": ["refs/tags/v*"], "exclude": [] } },
  "bypass_actors": [ { "actor_id": 5, "actor_type": "RepositoryRole", "bypass_mode": "always" } ],
  "rules": [ { "type": "creation" } ]
}
JSON
```

Also make published releases immutable (their assets and tag cannot be
changed after publishing):

```
gh api -X PUT "repos/$R/immutable-releases"
```

Check all of it:

```
gh api "repos/$R/rulesets" --jq '.[] | [.id, .name, .target, .enforcement] | @tsv'
gh api "repos/$R/rules/branches/dev"  --jq '.[].type'
gh api "repos/$R/rules/branches/main" --jq '.[].type'
gh api "repos/$R/immutable-releases"
```

and open Settings → Rules → Rulesets once to see that the tag-creation
ruleset's bypass list shows "Repository admin".

## 6. Actions settings

Only GitHub's own actions may run (the workflows use nothing else; gitleaks
and actionlint are downloaded as checksummed binaries), every action must be
pinned to a full commit SHA, the default token stays read-only and cannot
approve pull requests, and a workflow run from any outside contributor's pull
request waits for a maintainer's approval.

```
gh api -X PUT "repos/$R/actions/permissions" \
  -F enabled=true -f allowed_actions=selected -F sha_pinning_required=true
gh api -X PUT "repos/$R/actions/permissions/selected-actions" \
  -F github_owned_allowed=true -F verified_allowed=false
gh api -X PUT "repos/$R/actions/permissions/workflow" \
  -f default_workflow_permissions=read -F can_approve_pull_request_reviews=false
gh api -X PUT "repos/$R/actions/permissions/fork-pr-contributor-approval" \
  -f approval_policy=all_external_contributors
```

Check:

```
gh api "repos/$R/actions/permissions"
gh api "repos/$R/actions/permissions/selected-actions"
gh api "repos/$R/actions/permissions/fork-pr-contributor-approval"
```

## 7. Dependabot

Version updates need nothing beyond `.github/dependabot.yml` on `main`: weekly,
grouped pull requests for Go modules and for the pinned actions, opened
against `dev`. Turn on the alerts and the security-fix pull requests as well:

```
gh api -X PUT "repos/$R/vulnerability-alerts"
gh api -X PUT "repos/$R/automated-security-fixes"
```

Security-fix pull requests are opened against the default branch (`main`),
not `dev`. Treat one as a hotfix: merge it into `main` only as part of a
patch release, and cherry-pick it into `dev`.

Check:

```
gh api -i "repos/$R/vulnerability-alerts" | head -1     # HTTP/2.0 204
gh api "repos/$R/automated-security-fixes"              # "enabled": true
```

Secret scanning and push protection are already on; private vulnerability
reporting is already on.

## 8. Labels for the release notes

```
gh label create skip-changelog --repo "$R" --color ededed \
  --description "Leave this pull request out of the release notes"
gh label create dependencies --repo "$R" --color 0366d6 \
  --description "Dependency updates" 2>/dev/null || true
```

`bug` and `enhancement` exist in every new repository.
