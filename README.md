# AgentSync

> ## Don't be loyal to companies.
>
> Use whatever tool you want, wherever you want, however you want.
> Your rules, your MCP servers, your skills, your permission policy are **yours** —
> not a product feature, not a moat, not someone's subscription tier.
> Stop feeding proprietary silos and stop hand-copying your own config into them.
>
> Switch agents like you switch editors. Your setup follows you.

AI coding agents want you to live inside their world: their config directory, their format, their little ecosystem. The moment you try a different agent, you start from zero. That is not an accident — lock-in is the business model.

**AgentSync treats every agent as a replaceable client of your vault.** Your configuration lives in one place, in plain files you own. Agents come and go; the vault stays.

```
~/.agent-sync/                  your vault — the only thing you edit
├── rules/base.md               your rules and instructions
├── mcp/servers.json            MCP servers (canonical, portable form)
├── mcp/secrets.json            credential values (0600, never committed)
├── skills/<name>/              skills, whole trees
├── permissions/rules.json      permission policy (opt-in)
├── objects/                    content-addressed history
└── state.json                  sync state: per-agent bases, conflicts, snapshots
        ▲                             │
        │  pull changes               │  push changes
   ┌────┴─────┬───────────┬───────────┴────┐
 Claude Code  OpenCode   Gemini CLI     Cursor
```

## What it does

Edit config in **any** agent — or directly in the vault — and everything else follows. Real files plus a git-like 3-way merge: we never create symlinks and never replace the ones you have.

- **Rules / instructions** — one markdown canon, rendered into each agent's rules file.
- **MCP servers** — one portable canonical form; agent-only fields (`timeout`, `oauth`, `enabled`, `trust`, …) stay in the agent's own file and survive every rewrite.
- **Skills** — whole directory trees, 3-way merged file by file. Plugin caches are skipped; symlinked skills are read but never overwritten.
- **Permissions** (opt-in) — tool/shell/MCP rules as a set, merged with `deny > ask > allow`. Agent-local defaults (`*: allow`) and path globs stay local by design.

Conflicts are explicit, never silent: a disagreeing server, skill or rule is held back from that agent while everything else keeps syncing. `agent-sync conflicts` lists the open ones; `agent-sync resolve <id> --take vault|agent|file` settles them. Text conflicts get git-style markers in `conflicts/`.

## Supported agents

| Agent | Rules | MCP | Skills | Permissions |
|---|---|---|---|---|
| Claude Code | ✅ `~/.claude/CLAUDE.md` | ✅ `~/.claude.json` | ✅ writes `~/.claude/skills` | ✅ opt-in (tools, shell, MCP) |
| OpenCode | ✅ `~/.config/opencode/AGENTS.md` | ✅ `opencode.jsonc` (comments preserved) | reads Claude/shared dirs natively | ✅ opt-in (tools, shell, MCP) |
| Gemini CLI | ✅ `~/.gemini/GEMINI.md` | ✅ `settings.json` (`httpUrl`) | ✅ `~/.gemini/skills` | ✅ opt-in (shell rules) |
| Cursor | — (user rules live in the account) | ✅ `~/.cursor/mcp.json` | reads Claude/shared dirs natively | ✅ opt-in (shell, MCP) |

Claude Code project-scope MCP (`.mcp.json` in a repository, `projects.*` in `~/.claude.json`) is never written: `doctor` reports collisions with the vault, because project scope exists precisely to differ between repositories.

## Install

```bash
git clone git@github.com:odiumuniverse/agents-sync.git
cd agents-sync
make build          # bin/agent-sync
```

## Quick start

```bash
agent-sync init                 # detect agents, create the vault
agent-sync sync                 # pull every agent into the vault, push the union back
agent-sync status               # resources, conflicts, agents
agent-sync diff                 # what a sync would change, item by item
agent-sync conflicts            # what waits for a decision
agent-sync doctor               # broken symlinks, permissions, drift, collisions
```

Run it continuously:

```bash
agent-sync watch                                  # foreground watcher
agent-sync daemon install                         # launchd (macOS) / systemd user unit (Linux)
```

Or let each agent trigger it — the watcher watches agent files too, not just the vault.

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
| `agents` (`enable`/`disable`/`mode`) | agents and their per-kind modes |
| `kinds` | switch kinds on or off for every agent |
| `doctor` | diagnostics; non-zero exit on errors |
| `watch` / `daemon` | background sync, autostart service |
| `secrets list\|set\|rm\|prune` | credential values; never printed |

Global flags: `--vault` (default `~/.agent-sync`, or `$AGENTSYNC_HOME`), `--verbose`, `--log-json`.

## Design rules

- **Real files, symlinks respected.** We never create symlinks and never replace yours: a write goes through a link to its target; broken links are reported by `doctor`.
- **Nothing is deleted silently.** Deletions propagate only relative to each agent's own base, and a mass deletion waits for confirmation as a conflict.
- **JSONC is kept JSONC.** Comments and formatting of untouched parts survive edits.
- **Atomic writes.** tmp file + fsync + rename in the target directory, then fsync of the directory; the file mode is preserved.
- **Local stays local.** OAuth state, hooks, plugins, UI settings, folder trust — untouched. Secrets live in `mcp/secrets.json` (0600) and never reach the synced canon, the object store or git history.
- **Vendor formats are guests.** `pkg/kind` defines how each resource merges; `pkg/agent` translates an agent's own format and keeps everything it does not own.

## Development

```bash
make test    # go test -race ./...
make lint    # golangci-lint (48 linters)
make fmt
make mod     # go mod tidy && go mod vendor (deps are vendored)
```

Layout: `cmd/agentsync`, `pkg/{cli,vault,config,state,cas,fsutil,merge,kind,agent,engine,lock,watch,daemon,history,secret,mcp,skill,permission}`.
Docs: [architecture](docs/ARCHITECTURE.md) · [roadmap](docs/PLAN.md) · [v2 audit](docs/AUDIT.md).

## License

MIT. Take it, fork it, replace the vendors.
