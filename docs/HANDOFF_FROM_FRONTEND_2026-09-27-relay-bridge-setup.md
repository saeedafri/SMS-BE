# Handoff — set up relay-bridge so this repo can receive automated handoffs

**Date:** 27 September 2026
**From:** Frontend team
**To:** Saeed / backend team

---

## What this is

We've built a small tool, **relay-bridge**, that lets the frontend and backend
teams hand work off to each other's Claude Code session automatically —
fully automatically, both directions: nobody runs a command, nobody copies a
handoff doc into a chat. Commit a handoff file, push it, and the other side's
Claude Code picks it up, does the work, and opens a PR for review. Nobody
needs to be at their laptop for any of it.

Full design + source: <https://github.com/SAQIBJH/relay-bridge> (we've
invited your GitHub account as a collaborator so you can clone it — accept
the invite, then `git clone` gets you `send_handoff.py` for manual/ad-hoc
sends, though you shouldn't need it day to day once this is set up).

**This PR already contains both pieces for this repo:**
- `.github/workflows/handoff.yml` — receives a handoff *from* us and runs your
  Claude Code against it.
- `.github/workflows/handoff-sender.yml` — automatically sends a handoff *to*
  us the moment you push a `docs/HANDOFF_TO_UI_*.md` file (your own existing
  naming convention — nothing changes about how you already work). No command
  to run; it just fires on push.

Everything below is what's left, and it's written so your own Claude Code —
given permission — can run almost all of it itself. Paste this file's path to
your Claude Code session and say "do this," and it can execute every command
below; the only things that need *you* specifically are two browser approval
clicks, which are called out clearly.

## What we could NOT do from our side, and why

We (SAQIBJH) only have **read** access to this repo, not write — verified via
the GitHub API, not assumed. That's exactly right and we didn't try to work
around it. So we opened this as a pull request from a fork instead of pushing
directly. You'll need to review and merge it, same as any other PR.

Two credentials in the steps below fundamentally require *your* subscription
or *your* click — nobody else can generate them for you:
- `CLAUDE_CODE_OAUTH_TOKEN` must come from **your own** Claude Pro/Max
  subscription login.
- Installing the Claude GitHub App and generating a fine-grained PAT both
  require a one-time browser approval — GitHub doesn't expose an API for
  either, by design.

## Step-by-step (run these in order; your Claude Code can execute all the commands, you just approve in the browser when one opens)

### 1. Review this PR

Look at `.github/workflows/handoff.yml`. It's configured with:
- `CHECKOUT_SOURCE_REPO: "true"` and `SOURCE_CHECKOUT_LINK: "../SMS-UI"` —
  matches this repo's own `CLAUDE.md` rule #1 (pull the frontend checkout
  before doing anything).
- `--allowedTools "Read,Write,Edit,Glob,Grep,Bash(git:*),Bash(gh:*)"` and
  `--permission-mode dontAsk` — this is **your** autonomy dial. Tighten or
  loosen it to whatever you're comfortable granting an unattended run. If you
  want it to also run your remote test suite (`make test` via
  `scripts/remote-test.sh`), add whatever `Bash(...)` pattern that needs, and
  see step 5 about the SSH key that needs.
- It never pushes to `main`/`master` directly — only ever opens a PR. That's
  enforced by the prompt, not by GitHub config, so **please add a branch
  protection rule on `main`** as the real backstop (Settings → Branches →
  Add rule). This was a deliberate, reviewed trade-off on our side, not an
  oversight.

### 2. Install the Claude GitHub App on this repo

```
/install-github-app
```

Run this in your Claude Code. It'll open a browser — **this is the one step
that needs you personally**: pick this repo (`saeedafri/SMS-BE`) and approve
the installation. `claude-code-action` (used in the workflow) authenticates
as this app by default, and it has to already be installed before the first
dispatch arrives.

### 3. Generate your subscription token

```bash
claude setup-token
```

This also opens a browser for you to approve — **second and last manual
click**. It prints a long-lived token in the terminal afterward. Your Claude
Code can capture that output and immediately run:

```bash
gh secret set CLAUDE_CODE_OAUTH_TOKEN --repo saeedafri/SMS-BE
```

(paste the printed token when prompted, or pipe it in) — this needs you to
have `admin` on this repo, which as the repo owner you do.

### 4. `SOURCE_REPO_READ_TOKEN` — we'll send you this one, you just store it

**Correction from an earlier version of this doc:** this can't be something
you generate yourself — a fine-grained PAT can only be scoped to a repo the
*creator* has access to, and you don't have access to our frontend repo. So
**we're generating it** (Contents: Read-only, scoped only to
`SAQIBJH/sms-platform-frontend`) and will send you the value directly
(not over this PR — somewhere private, like Slack/DM).

Once you have it, either paste it to your Claude Code and let it run:

```bash
gh secret set SOURCE_REPO_READ_TOKEN --repo saeedafri/SMS-BE
```

or add it yourself under Settings → Secrets and variables → Actions → New
repository secret.

### 4b. What we need back from you: TWO tokens for this repo

**Second correction:** we tried to generate a read-only token scoped to this
repo ourselves too (for our own `SOURCE_REPO_READ_TOKEN`, used when *you* send
a handoff to *us* — the reverse direction). Turned out we can't — a
fine-grained PAT can only be scoped to a repo you own, or one under an
organization that allows it. This repo is under your personal account, so
even though we're a collaborator with read access, it doesn't show up in our
"select repositories" list at all. Only you can create a token scoped to your
own repo, full stop — regardless of what permission level it grants.

So, two tokens, both scoped to `saeedafri/SMS-BE`, both generated the same way:

1. Go to <https://github.com/settings/personal-access-tokens/new>
2. Repository access → "Only select repositories" → `saeedafri/SMS-BE`
3. Generate **two separate tokens** (give each a distinct name in the "Token
   name" field — that name is just for your own reference, it doesn't need to
   match anything):
   - One with Permissions → Repository permissions → **Contents: Read-only**
     — this is our `SOURCE_REPO_READ_TOKEN`
   - One with Permissions → Repository permissions → **Contents: Read and
     write** — this is our dispatch token
4. Send us both values the same private way (not as a PR comment) — tell us
   clearly which is which

We'll store the dispatch one as a repo secret in our own frontend repo (so our
`handoff-sender.yml` can use it automatically). Nothing works end-to-end until
we have this.

### 4c. What you need to store here: our dispatch token, for your sender to work

Symmetrically, `handoff-sender.yml` (added in this PR) needs a secret
`RELAY_BRIDGE_DISPATCH_TOKEN` **in this repo** — a token with write access to
*our* frontend repo, so it can dispatch to us when you push a handoff. We're
generating this ourselves (same reasoning as 4b, reversed: only we can mint a
token scoped to our own repo) and will send it to you the same private way.
Once you have it:

```bash
gh secret set RELAY_BRIDGE_DISPATCH_TOKEN --repo saeedafri/SMS-BE
```

### 5. (Optional) test-tunnel SSH key, only if you want the remote suite in CI

If you widen `--allowedTools` in step 1 to let the agent run
`scripts/remote-test.sh` / `make test`, it'll need SSH access to the AWS box
to reach Postgres/ClickHouse/Redis. **Don't reuse `deploy.yml`'s
`AWS_SSH_KEY`** — mint a separate key/user scoped to read/tunnel access only,
so an autonomous run can never hold the credential that restarts
`relay-api` in production. Add it as its own secret and reference it in the
workflow.

### 6. (Optional) `PR_TOKEN`

GitHub doesn't run this repo's own `pull_request`-triggered CI/review
workflows on a PR opened with the default `GITHUB_TOKEN`. If you want your
usual CI/Codex review to fire automatically on the PRs this workflow opens,
add a PAT with contents+PR write on this repo as secret `PR_TOKEN`. If you
skip this, the PRs still get opened fine — your CI just won't auto-run on
them; you can trigger it manually.

### 7. Merge this PR

Once secrets are in place, merge it. The workflow is live from that point.

## How to test it

Once you've merged and added the secrets, push a trivial test file matching
`docs/HANDOFF_TO_UI_*.md` from your side, or ask us to push a
`docs/HANDOFF_TO_BACKEND_*.md` test file from ours — either way, watch the
Actions tab of the receiving repo. A PR should appear within a minute or two
of the push, with no command run by hand.

## The reverse direction is already wired up too

We've already added `handoff.yml` and `handoff-sender.yml` to our own
frontend repo (mirroring what's in this PR), so once the token exchange in
steps 4/4b/4c is done, **both directions run fully automatically**: you commit
a `docs/HANDOFF_TO_UI_*.md` file and push — that's it, it reaches our Claude
Code with no command from you. Same for us sending to you.
