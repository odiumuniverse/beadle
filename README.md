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
├── mcp/servers.json            MCP servers (canonical form)
├── skills/<name>/              skills, whole trees
├── permissions/rules.json      permission policy (opt-in)
├── objects/                    content-addressed history
└── registry.json               sync state
        ▲                             │
        │  pull changes               │  push changes
   ┌────┴─────┬───────────┬───────────┴────┐
 Claude Code  OpenCode   Gemini CLI     Cursor
```

## What it does

Edit config in **any** agent — or directly in the vault — and everything else follows. No symlinks, no copies drifting apart: real files plus a git-like 3-way merge.

- **Rules / instructions** — one markdown canon, rendered into each agent's rules file.
- **MCP servers** — canonical superset of every agent's schema; per-agent fields (`timeout`, `oauth`, `enabled`, …) survive round trips.
- **Skills** — whole directory trees, 3-way merged file by file. Plugin-managed skills and symlinks are skipped, never swallowed.
- **Permissions** (opt-in) — tool/shell/MCP rules as a set, merged with `deny > ask > allow`. Agent-local defaults (`*: allow`) and path globs stay local by design.

Conflicts are explicit, never silent: markdown gets conflict markers, JSON resources keep the vault version and store the agent variant for `resolve --keep-agent`.

## Supported agents

| Agent | Rules | MCP | Skills | Permissions |
|---|---|---|---|---|
| Claude Code | ✅ `~/.claude/CLAUDE.md` | ✅ `~/.claude.json` | ✅ writes `~/.claude/skills` | ✅ opt-in (tools, shell, MCP) |
| OpenCode | ✅ `~/.config/opencode/AGENTS.md` | ✅ `opencode.jsonc` (comments preserved) | reads Claude/shared dirs natively | ✅ opt-in (tools, shell, MCP) |
| Gemini CLI | ✅ `~/.gemini/GEMINI.md` | ✅ `settings.json` (`httpUrl`) | ✅ `~/.gemini/skills` | ✅ opt-in (shell rules) |
| Cursor | — (user rules live in the account) | ✅ `~/.cursor/mcp.json` | reads Claude/shared dirs natively | ✅ opt-in (shell, MCP) |

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
agent-sync diff                 # what differs right now
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
| `init` | create the vault, register detected agents |
| `sync` / `pull` / `push` | full cycle / agents → vault / vault → agents |
| `status` / `diff` | registry state / current differences |
| `resolve <resource>` | finish a conflict (`--keep-agent`, `--agent`) |
| `restore <resource> [--rev]` | roll a resource back to its base blob |
| `doctor` | diagnostics; non-zero exit on errors |
| `watch` / `daemon` | background sync, autostart service |
| `sync --prune` | also delete agent skills that are absent from the vault |

Global flags: `--vault` (default `~/.agent-sync`, or `$AGENTSYNC_HOME`), `--verbose`, `--log-json`.

## Design rules

- **No symlinks.** Copies and merges; broken links are reported by `doctor`, never created.
- **Nothing is deleted silently.** Whole resources require explicit `--prune`; deletions inside a resource follow the 3-way base.
- **JSONC is kept JSONC.** Comments and formatting of untouched parts survive edits.
- **Atomic writes.** tmp file + fsync + rename in the target directory, then fsync of the directory.
- **Local stays local.** Secrets, OAuth state, hooks, plugins, UI settings, folder trust — untouched.
- **Vendor formats are guests.** Canonical models live in `pkg/mcp`, `pkg/permission`, `pkg/skill`; adapters translate.

## Development

```bash
make test    # go test -race ./...
make lint    # golangci-lint (48 linters)
make fmt
make mod     # go mod tidy && go mod vendor (deps are vendored)
```

Layout: `cmd/agentsync`, `pkg/{cli,vault,config,registry,cas,fsutil,merge,mcp,skill,permission,adapter,sync,watch,daemon}`.
Design document: [SPEC.md](SPEC.md).

## License

MIT. Take it, fork it, replace the vendors.
