# Beadle

> ## Don't be loyal to companies.
>
> Use whatever tool you want, wherever you want, however you want.
> Your rules, your MCP servers, your skills, your permission policy are **yours** —
> not a product feature, not a moat, not someone's subscription tier.
> Stop feeding proprietary silos and stop hand-copying your own config into them.
>
> Switch agents like you switch editors. Your setup follows you.

AI coding agents want you to live inside their world: their config directory, their format, their little ecosystem. The moment you try a different agent, you start from zero. That is not an accident — lock-in is the business model.

**Beadle treats every agent as a replaceable client of your vault.** Your configuration lives in one place, in plain files you own. Agents come and go; the vault stays.

```
~/.beadle/                  your vault — the only thing you edit
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
  + Antigravity CLI
```

## What it does

Edit config in **any** agent — or directly in the vault — and everything else follows. Real files plus a git-like 3-way merge: we never create symlinks and never replace the ones you have.

- **Rules / instructions** — one markdown canon, rendered into each agent's rules file.
- **MCP servers** — one portable canonical form; agent-only fields (`timeout`, `oauth`, `enabled`, `trust`, …) stay in the agent's own file and survive every rewrite.
- **Skills** — whole directory trees, 3-way merged file by file. Plugin caches are skipped; symlinked skills are read but never overwritten.
- **Permissions** (opt-in) — tool/shell/MCP rules as a set, merged with `deny > ask > allow`. Agent-local defaults (`*: allow`) and path globs stay local by design.

Conflicts are explicit, never silent: a disagreeing server, skill or rule is held back from that agent while everything else keeps syncing. `beadle conflicts` lists the open ones; `beadle resolve <id> --take vault|agent|file` settles them. Text conflicts get git-style markers in `conflicts/`.

## Supported agents

| Agent | Rules | MCP | Skills | Permissions |
|---|---|---|---|---|
| Claude Code | ✅ `~/.claude/CLAUDE.md` | ✅ `~/.claude.json` | ✅ writes `~/.claude/skills` | ✅ opt-in (tools, shell, MCP) |
| OpenCode | ✅ `~/.config/opencode/AGENTS.md` | ✅ `opencode.jsonc` (comments preserved) | reads Claude/shared dirs natively | ✅ opt-in (tools, shell, MCP) |
| Gemini CLI | ✅ `~/.gemini/GEMINI.md` | ✅ `settings.json` (`httpUrl`) | ✅ `~/.gemini/skills` | ✅ opt-in (shell rules) |
| Cursor | — (user rules live in the account) | ✅ `~/.cursor/mcp.json` | reads Claude/shared dirs natively | ✅ opt-in (shell, MCP) |
| Antigravity CLI | — (reads `~/.gemini/GEMINI.md` via the Gemini CLI adapter) | ✅ `~/.gemini/config/mcp_config.json` (`serverUrl`) | — (v1 not managed; agy reads `~/.gemini/antigravity-cli/skills` and `.agents/skills`) | — (not managed: `action(target)` dialect) |

Project scope is opt-in per repository: `beadle project enable <file>` records the file in the vault-side policy of the current project, and `beadle project status|disable|forget` manages it. Managed files: `.mcp.json`, `.cursor/mcp.json`, `.cursor/rules/*.mdc`, `.agents/mcp_config.json`, `AGENTS.md` and `GEMINI.md`. The project identity comes from the git origin (clones and worktrees converge), or from the path for non-git directories. Without a policy nothing in a repository is touched; `doctor` keeps reporting foreign `.mcp.json`/`projects.*` collisions. Tracked files never receive secret values: references render as `${NAME}` and only gitignored files (or files enabled with `--allow-secrets`) materialize values. Deleting a project file is not a deletion: it is reported as `kept` until `beadle project forget <file>`.

## Install

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
| `heal [--dry-run]` | clear quarantined plugins: stubs, artifacts, ledger tombstones |
| `agents` (`enable`/`disable`/`mode`) | agents and their per-kind modes |
| `kinds` | switch kinds on or off for every agent |
| `doctor` | diagnostics; non-zero exit on errors |
| `watch` / `daemon` | background sync, autostart service |
| `secrets list\|set\|rm\|prune\|migrate` | credential values; never printed |
| `project status\|enable <file>\|disable <file>\|forget <file>` | per-repository project scope; nothing happens without an enabled policy |

Global flags: `--vault` (default `~/.beadle`, or `$BEADLE_HOME`), `--verbose`, `--log-json`.

## Design rules

- **Real files, symlinks respected.** We never create symlinks and never replace yours: a write goes through a link to its target; broken links are reported by `doctor`.
- **Nothing is deleted silently.** Deletions propagate only relative to each agent's own base, and a mass deletion waits for confirmation as a conflict.
- **JSONC is kept JSONC.** Comments and formatting of untouched parts survive edits.
- **Atomic writes.** tmp file + fsync + rename in the target directory, then fsync of the directory; the file mode is preserved.
- **Local stays local.** OAuth state, hooks, plugins, UI settings, folder trust — untouched. Secrets live in `mcp/secrets.json` (0600) and never reach the synced canon, the object store or git history.
- **Secrets can live in the OS keychain.** `beadle secrets migrate keyring` moves the values into the macOS keychain (`security`) or Linux `secret-tool`; the file then holds names only (`backend: keyring`). The keyring is opt-in and fail-closed: an unavailable or locked keyring yields skips and warnings — never a plaintext fallback. `secrets migrate file` brings the values back; deleting `secrets.json` returns to the file backend.
- **Vendor formats are guests.** `pkg/kind` defines how each resource merges; `pkg/agent` translates an agent's own format and keeps everything it does not own.

## Development

```bash
make test    # go test -race ./...
make lint    # golangci-lint (48 linters)
make fmt
make mod     # go mod tidy && go mod vendor (deps are vendored)
```

Layout: `cmd/beadle`, `pkg/{cli,vault,config,state,cas,fsutil,merge,kind,agent,engine,lock,watch,daemon,history,secret,mcp,skill,permission}`.

## License

MIT. Take it, fork it, replace the vendors.
