# Beadle

Beadle keeps your AI coding agents configured from one place, so switching
tools or machines doesn't mean starting over.

Every agent stores its own instructions, MCP servers, skills and permission
rules in its own format. Use two agents, or one agent on two machines, and
you start copying the same config by hand and fixing the drift. Beadle keeps
that configuration in a private vault of plain files you own, syncs it both
ways with a git-style three-way merge, and leaves everything it doesn't
manage alone.

```
~/.beadle/                  your vault — the only thing you edit
├── rules/base.md               your rules and instructions
├── mcp/servers.json            MCP servers (canonical, portable form)
├── mcp/secrets.json            credential values (0600, never committed)
├── skills/<name>/              skills, whole trees
├── permissions/rules.json      permission policy (opt-in)
├── memory/<slug>/              Claude per-project notes
├── projects/<repo-id>/         per-repository files and policy
├── objects/                    content-addressed history
└── state.json                  sync state: per-agent bases, conflicts, snapshots
        ▲                             │
        │  pull changes               │  push changes
   ┌────┴─────┬───────────┬───────────┴────┐
 Claude Code  OpenCode   Gemini CLI     Cursor
  + Antigravity CLI
```

## Why use it

- **One config, several agents.** Edit rules, servers or skills in whichever
  agent you happen to be in — or directly in the vault — and the rest follow.
- **Merges, not overwrites.** Real files with a three-way merge against each
  agent's last known state; disagreements become explicit conflicts instead of
  silent losses. Nothing you wrote is deleted without a decision.
- **Multiple machines.** The vault is a directory; put it in git (or your own
  sync) and every machine shares the same canon. Credential values stay out of
  it.
- **Your own repository is respected.** Project scope is opt-in per repo and
  leak-aware: secret values only land in gitignored files, generated blocks
  skip tracked files, and unmanaged entries are never rewritten.

## What it does

- **Rules and instructions** — one markdown canon rendered into each agent's
  rules file (`CLAUDE.md`, `AGENTS.md`, `GEMINI.md`). *Why:* the thing agents
  read first is usually the thing most out of date.
- **MCP servers** — one portable canonical form; agent-only fields (`timeout`,
  `oauth`, `enabled`, `trust`, …) stay in the agent's own file and survive every
  rewrite. *Why:* server lists drift across agents and machines fastest.
- **Secrets** — values are extracted into `mcp/secrets.json` (0600, never
  committed) or the OS keychain (`secrets migrate keyring`); files carry
  `{secret:NAME}` references, and shareable files render `${NAME}` instead of a
  value. *Why:* configs get copied and committed; credentials shouldn't.
- **Skills** — whole directory trees, merged file by file. Plugin caches are
  skipped, symlinked skills are read but never overwritten, and plugin skills
  are farmed through a stable pivot so upgrades don't break links. *Why:*
  skills are the biggest pile of files to keep in sync.
- **Project scope** — opt-in per repository: `.mcp.json`, `.cursor/mcp.json`,
  `.cursor/rules/*.mdc`, `.agents/mcp_config.json`, `AGENTS.md`, `GEMINI.md`.
  The project identity comes from the git origin, so clones and worktrees
  converge; a missing file is never treated as a deletion (`project forget`
  removes scope). *Why:* the config that matters most is per-repo, and repos
  are where a personal vault can leak.
- **Claude memory** — per-project notes sync through the vault; a budget-capped
  digest lands in project files for the other agents; other agents' inbox lines
  become notes; a secret gate runs before anything is adopted. *Why:* only one
  agent remembers your project, and everyone else should benefit.
- **Permissions** (opt-in) — tool/shell/MCP rules as a set, merged with
  `deny > ask > allow`; agent-local defaults and path globs stay local.
  *Why:* permission policy is currently retyped in every agent.
- **Diagnostics and recovery** — `doctor` reports broken links, drift,
  collisions and half-finished states; `conflicts`/`resolve` settle
  disagreements; `history`/`restore` roll kinds back; `heal` clears quarantined
  plugins. *Why:* sync tools are only trusted if you can see and undo what they
  did.

## Supported agents

| Agent | Rules | MCP | Skills | Permissions |
|---|---|---|---|---|
| Claude Code | ✅ `~/.claude/CLAUDE.md` | ✅ `~/.claude.json` | ✅ writes `~/.claude/skills` | ✅ opt-in (tools, shell, MCP) |
| OpenCode | ✅ `~/.config/opencode/AGENTS.md` | ✅ `opencode.jsonc` (comments preserved) | reads Claude/shared dirs natively | ✅ opt-in (tools, shell, MCP) |
| Gemini CLI | ✅ `~/.gemini/GEMINI.md` | ✅ `settings.json` (`httpUrl`) | ✅ `~/.gemini/skills` | ✅ opt-in (shell rules) |
| Cursor | — (user rules live in the account) | ✅ `~/.cursor/mcp.json` | reads Claude/shared dirs natively | ✅ opt-in (shell, MCP) |
| Antigravity CLI | — (reads `~/.gemini/GEMINI.md` via the Gemini CLI adapter) | ✅ `~/.gemini/config/mcp_config.json` (`serverUrl`) | — (v1 not managed) | — (not managed: `action(target)` dialect) |

Project scope is opt-in per repository: `beadle project enable <file>` records
the file in the vault-side policy of the current project, and
`beadle project status|disable|forget` manages it. Without a policy nothing in
a repository is touched; `doctor` keeps reporting foreign `.mcp.json` /
`projects.*` collisions.

## Install

```bash
brew tap odiumuniverse/tap
brew trust odiumuniverse/tap
brew install beadle
```

Or build from source:

```bash
git clone git@github.com:odiumuniverse/beadle.git
cd beadle
make build          # bin/beadle
```

## Quick start

```bash
beadle init                 # detect agents, create the vault
beadle sync                 # pull every agent into the vault, push the union back
beadle status               # resources, conflicts, agents
beadle diff                 # what a sync would change, item by item
beadle conflicts            # what waits for a decision
beadle doctor               # broken symlinks, permissions, drift, collisions
```

Run it continuously:

```bash
beadle watch                                  # foreground watcher
beadle daemon install                         # launchd (macOS) / systemd user unit (Linux)
```

## Commands

| Command | Purpose |
|---|---|
| `init [--agents a,b]` | create the vault, register detected agents |
| `sync [--dry-run] [--kind k]` | full cycle: pull, merge, push |
| `pull` / `push` | one direction only (`push` overwrites local agent edits) |
| `status [--check]` / `diff [--agent id]` | agents, modes and conflicts / what a sync would change |
| `conflicts [id]` | open conflicts; with an id, the variants |
| `resolve [id…] --take vault\|agent\|file` | settle conflicts and push the decision |
| `history <kind>` / `restore <kind> --to N` | snapshots and rollbacks |
| `heal [--dry-run]` | clear quarantined plugins: stubs, artifacts, ledger tombstones |
| `agents` (`enable`/`disable`/`mode`) | agents and their per-kind modes |
| `kinds` | switch kinds on or off for every agent |
| `doctor` | diagnostics; non-zero exit on errors |
| `watch` / `daemon` | background sync, autostart service |
| `secrets list\|set\|rm\|prune\|migrate` | credential values; never printed |
| `project status\|enable <file>\|disable <file>\|forget <file>` | per-repository project scope; nothing happens without an enabled policy |

Global flags: `--vault` (default `~/.beadle`, or `$BEADLE_HOME`), `--verbose`,
`--log-json`.

## Principles

- **Real files, symlinks respected.** Beadle never creates symlinks and never
  replaces yours: a write goes through a link to its target; broken links are
  reported by `doctor`. Project files are stricter — a symlink, a hard link or
  a non-regular file is refused.
- **Nothing is deleted silently.** Deletions propagate only relative to each
  agent's own base, mass deletions wait as conflicts, and project files that
  disappear (a branch switch, `git clean`) are reported as `kept`, not removed.
- **JSONC is kept JSONC.** Comments and formatting of untouched parts survive
  edits.
- **Atomic writes.** tmp file + fsync + rename in the target directory, then
  fsync of the directory; the file mode is preserved.
- **Local stays local.** OAuth state, hooks, plugins, UI settings, folder trust
  — untouched. Secrets never reach the synced canon, the object store or git
  history; with `backend: keyring` the store itself is fail-closed — an
  unavailable keyring yields skips and warnings, never a plaintext fallback.
- **Formats are guests.** `pkg/kind` defines how each resource merges;
  `pkg/agent` translates an agent's own format and keeps everything it does not
  own.

## Development

```bash
make test    # go test -race ./...
make lint    # golangci-lint
make fmt
make mod     # go mod tidy && go mod vendor (deps are vendored)
```

Layout: `cmd/beadle`,
`pkg/{cli,vault,config,state,cas,fsutil,merge,kind,agent,engine,project,lock,watch,daemon,history,secret,mcp,skill,permission}`.

## License

MIT.
