<div align="center">

  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/beadle-dark.svg">
    <img src="assets/beadle-light.svg" width="72" height="72" alt="beadle">
  </picture>

  <h1>beadle</h1>

  <p><b>One configuration for every AI coding agent.</b><br>
  Merges, not overwrites — nothing you wrote is deleted without a decision.</p>

  <p>
    <a href="https://github.com/odiumuniverse/beadle/releases"><img alt="release" src="https://img.shields.io/github/v/release/odiumuniverse/beadle?style=flat-square&color=3E6E5C&labelColor=1F2328"></a>
    <a href="https://github.com/odiumuniverse/beadle/actions/workflows/ci.yml"><img alt="ci" src="https://img.shields.io/github/actions/workflow/status/odiumuniverse/beadle/ci.yml?branch=master&style=flat-square&label=ci&color=3E6E5C&labelColor=1F2328"></a>
    <img alt="go" src="https://img.shields.io/badge/go-1.27-3E6E5C?style=flat-square&labelColor=1F2328">
    <img alt="license" src="https://img.shields.io/badge/license-MIT-3E6E5C?style=flat-square&labelColor=1F2328">
  </p>

</div>

Every agent stores its own instructions, MCP servers, skills and permission
rules in its own format; use two agents, or one agent on two machines, and the
same config gets copied by hand and starts to drift. Beadle keeps that
configuration in **a private vault of plain files you own**, syncs it both ways
with a **git-style three-way merge**, and leaves **everything it does not
manage** alone.

```
~/.beadle/                  your vault — the only thing you edit
├── rules/base.md               your rules and instructions
├── mcp/servers.json            MCP servers (canonical, portable form)
├── mcp/secrets.json            credential values (0600, never committed)
├── skills/<name>/              skills, whole trees
├── subagents/<name>.md         custom subagents (all supported hosts)
├── commands/<name>.md          custom commands / prompt templates
├── permissions/rules.json      permission policy (default on)
├── memory/<slug>/              Claude per-project notes
├── projects/<repo-id>/         per-repository files and policy
├── objects/                    content-addressed history
└── state.json                  sync state: per-agent bases, conflicts, snapshots
        ▲                             │
        │  pull changes               │  push changes
   ┌────┴─────┬───────────┬───────────┴────┐
 Claude Code  OpenCode   Gemini CLI     Cursor
  + Antigravity CLI + Codex CLI + Pi + Kilo Code
```

[Why use it](#why-use-it) · [Supported agents](#supported-agents) ·
[Install](#install) · [Quick start](#quick-start) ·
[How it works](#how-it-works) · [Reference](#reference) ·
[Commands](#commands)

## Why use it

- **One config, several agents.** Edit rules, servers or skills in whichever
  agent you are in — or directly in the vault — and the rest follow.
- **Merges, not overwrites.** Three-way merges against each agent's last known
  state; disagreement becomes an explicit conflict, not a silent loss.
- **Multiple machines.** The vault is a directory: put it in git (or your own
  sync) and every machine shares the same canon. Credential values stay out.
- **Your repository is respected.** Project scope is leak-aware: `init`
  enables the files already present in a git checkout, secret values only land
  in gitignored files (a tracked file needs an explicit per-file opt-in),
  generated blocks skip tracked files, and unmanaged entries are never
  rewritten.

## Supported agents

| Agent | Rules | MCP | Skills | Permissions | Subagents | Commands |
|---|---|---|---|---|---|---|
| Claude Code | ✓ `~/.claude/CLAUDE.md` | ✓ `~/.claude.json` | ✓ writes `~/.claude/skills` | ✓ default on ³ | ✓ `~/.claude/agents/**` | ✓ `~/.claude/commands` (legacy) ⁷ |
| OpenCode | ✓ `~/.config/opencode/AGENTS.md` ¹ | ✓ `opencode.jsonc` ¹ | ✓ writes `~/.config/opencode/skills`; reads flat `<name>.md` ⁵ | ✓ default on ³ | ✓ `~/.config/opencode/agents`, legacy `agent/` read ⁶ | ✓ `~/.config/opencode/commands`, legacy `command/` read ⁷ |
| Gemini CLI | ✓ `~/.gemini/GEMINI.md` | ✓ `settings.json` (`httpUrl`) | ✓ `~/.gemini/skills` | ✓ default on ³ | ✓ `~/.gemini/agents` ⁶ | ✓ `~/.gemini/commands/*.toml` ⁷ |
| Cursor | — (user rules live in the account) | ✓ `~/.cursor/mcp.json` | reads Claude/shared dirs natively | ✓ default on ³ | ✓ `~/.cursor/agents` (`.md`/`.mdc`/`.markdown`) | — (not supported yet) |
| Antigravity CLI | — (`~/.gemini/GEMINI.md` via the Gemini CLI adapter) | ✓ `~/.gemini/config/mcp_config.json` (`serverUrl`) | — (v1 not managed) | — ³ | ✓ `~/.gemini/config/agents` (global) | — (v1 not managed) |
| Codex CLI | ✓ `~/.codex/AGENTS.md` | ✓ `~/.codex/config.toml` ² | reads `~/.agents/skills` natively | — ³ | ✓ `~/.codex/agents/*.toml` (span-edit) | ✓ pull-only `~/.codex/prompts` (deprecated) ⁷ |
| Pi | ✓ `~/.pi/agent/AGENTS.md` | ✓ `~/.pi/agent/mcp.json` ⁴ | reads `~/.agents/skills` natively; flat `<name>.md` ⁵ | — (not documented) | — (no sub-agents by design) | ✓ `~/.pi/agent/prompts` ⁷ |
| Kilo Code | ✓ `~/.config/kilo/AGENTS.md` | ✓ `kilo.jsonc` ² | reads `~/.agents/skills` natively | ✓ default on ³ | ✓ `~/.config/kilo/agents`, legacy `agent/` and `mode(s)/` read ⁶ | ✓ `~/.config/kilo/commands`, legacy `command/` read ⁷ |
| DeepSeek Harness | ✓ `~/.dsh/AGENTS.md` (write) + project chain `AGENTS.md`/`CLAUDE.md` (pull, root → cwd) | — (non-goal, A-41; `cordis.patch.yml` — Q-16) | reads `~/.dsh/skills` (pull-only until A-39) | — (runtime knobs, non-goal A-41) | — (code providers, non-goal A-41) | — (plugin code, non-goal A-41) |

¹ OpenCode: V2 reads `AGENTS.md` only; MCP is `mcp.servers` in V2 and `mcp` in
  V1 — merged as one union, comments in `opencode.jsonc` preserved.

² Codex keeps its servers in the `mcp_servers` table, Kilo under the `/mcp`
  pointer. Comments and formatting are preserved when beadle edits the file.

³ Permission dialects beadle can apply: Claude Code, Kilo Code and OpenCode
  (tools, shell and MCP; `permission` in V1, `permissions` in V2) — Gemini CLI
  takes shell rules, Cursor shell and MCP. Antigravity's `action(target)`
  dialect and Codex's scalar `approval_policy`/`sandbox_mode` are not managed.
  The permissions kind is synchronized by default since config v3; opt out with
  `beadle kinds disable permissions`.

⁴ Pi has no built-in MCP: beadle writes `~/.pi/agent/mcp.json`, which only the
  third-party `pi-mcp-adapter` reads; `doctor` warns when managed servers are
  present and the adapter is not detected.

⁵ Flat skills: OpenCode and Pi also read a `<name>.md` file in their own skills
  directory as a skill named by the file. Beadle adopts such copies into the
  canon and keeps delivering directories; when a same-name directory exists,
  it wins and the file is left untouched (`doctor` reports both).

⁶ Subagents: the canon is `<vault>/subagents/<name>.md` (frontmatter + system
  prompt). OpenCode and Kilo Code get a strict V2 frontmatter (`permissions`
  instead of `tools`); nested ids and inline `agents` in `opencode.json(c)` are
  not synced yet, `doctor` reports both. Gemini `kind: remote` agents and the
  workspace subagent directories (`.gemini/agents`, `.agents/agents`,
  `.cursor/agents`, `.codex/agents`) are v1 non-goals. Codex files are edited
  surgically — comments and foreign keys stay.

⁷ Commands: the canon is `<vault>/commands/<name>.md` (frontmatter +
  template). Beadle shifts positional arguments between Claude's 0-based `$0`
  and the 1-based canon, translates Gemini's `{{args}}`/`!{cmd}`/`@{path}`,
  and skips a host whose dialect cannot express a placeholder (doctor
  explains). Codex prompts are deprecated: beadle pulls them into the vault
  but never writes them back; Cursor file commands are not supported yet.

A ✓ means beadle writes that surface. `reads …` means the agent reads a
shared directory itself (`~/.claude/skills`, `~/.agents/skills`), and those
skills surfaces default to the `pull` mode — the agent's own skills directory
is not written. The shared `~/.agents/skills` surface is enabled by `beadle
init` and the config v3 migration (mode `sync`): it is the delivery channel for
the agents that read it natively. Opt out with `beadle agents disable shared`.

Beadle creates only the surfaces marked creatable: the rules files of Claude
Code and Gemini CLI, their skills directories, the Codex `config.toml`, the
subagent directories of Claude Code, OpenCode, Kilo Code, Codex, Gemini CLI,
Antigravity and Cursor, and the shared `~/.agents/skills` directory. Every
other surface is written only when
its config file already exists — an agent discovered by its directory alone is
skipped with `no config file to write into; create <path> first` until the file
appears (an empty file is enough).

## Install

```bash
brew tap odiumuniverse/tap
brew trust odiumuniverse/tap
brew install beadle
```

Homebrew does not start services on install. To keep the watcher running:
`brew services start beadle` — it serves the default vault `~/.beadle`; for a
custom vault use `beadle daemon install` instead.

Or build from source:

```bash
git clone git@github.com:odiumuniverse/beadle.git
cd beadle
make build          # bin/beadle
```

## Quick start

```bash
beadle init                 # create the vault, enable detected agents, wire the defaults
beadle sync                 # pull every agent into the vault, push the union back
beadle status               # resources, conflicts, agents
beadle diff                 # what a sync would change, item by item
beadle conflicts            # what waits for a decision
beadle doctor               # broken symlinks, permissions, drift, collisions
```

`beadle init` wires the defaults on a fresh machine: detected agents are
enabled, the native bundles of detected hosts are rendered and registered (a
host without its CLI gets the registration command instead and keeps file
sync), the present project files of a git checkout are enabled with secrets
kept out, and the background watcher is installed unless one is already there.
`--no-daemon` skips the watcher; `--daemon` reinstalls it even when a service
file exists. `beadle daemon install` does the same later.

Upgrading from config v2: the first command that saves the config flips
`kinds.permissions` to `sync` and enables the shared skills surface once, with
a `config: …` line on stderr; hooks already in the vault canon are approved in
the same pass (a `note: …` line). Opt out per feature: `beadle kinds disable
permissions`, `beadle agents disable shared`, `beadle hooks revoke <name>`,
`beadle bundles disable <host>`.

`config.json` is created with explicit defaults; the optional settings
(`permissions`, `history`, `secrets`) are always visible.

Run it continuously:

```bash
beadle watch                                  # foreground watcher
beadle daemon install                         # launchd (macOS) / systemd user unit (Linux)
```

## How it works

Each agent keeps its own files; beadle also remembers what each agent last saw
— its *base*. A sync pulls each agent's files into the vault, merges them
against the base with a three-way merge, and pushes the union back:

```
agent files ──pull──▶ vault canon ──3-way merge──▶ vault canon ──push──▶ agent files
                                         ▲
                                   per-agent base
```

When both sides changed the same item the merge stops: it becomes a conflict
that waits for a human or an agent (`beadle conflicts`, `beadle resolve`)
instead of overwriting either side; files beadle does not manage are untouched.
See [ARCHITECTURE.md](ARCHITECTURE.md) for the picture.

## Reference

What beadle manages, one line per kind; open a block for the mechanics and the
limits.

<details>
<summary><b>Rules and instructions</b> — one markdown canon rendered into every agent's rules file</summary>

`CLAUDE.md`, `AGENTS.md`, `GEMINI.md`. *Why:* the thing agents read first is
usually the thing most out of date.

</details>

<details>
<summary><b>MCP servers</b> — one portable canonical form; agent-only fields survive every rewrite</summary>

`timeout`, `oauth`, `enabled`, `trust` and the like stay in the agent's own
file. New entries are rendered with the file's own indentation — a minified
file stays minified. *Why:* server lists drift across agents and machines
fastest.

</details>

<details>
<summary><b>Secrets</b> — credential values live outside the canon, files carry <code>{secret:NAME}</code> references</summary>

The gate covers MCP, memory and project files; rules, skills and permissions
are not scanned, and `doctor` warns about secret-like lines in rules.
*Why:* configs get copied and committed; credentials should not.

See [Secrets](#secrets).

</details>

<details>
<summary><b>Skills</b> — whole directory trees, merged file by file</summary>

Plugin caches are skipped; symlinked skills are read but never overwritten;
plugin skills are farmed through a stable pivot so upgrades do not break
links. Plugin agents and commands are farmed the same way (`<plugin>--<name>.md`
for markdown hosts, a rendered TOML copy for Codex agents and Gemini commands;
Claude reads plugin agents and commands natively, Codex prompts are pull-only).
*Why:* skills are the biggest pile of files to keep
in sync.

</details>

<details>
<summary><b>Plugin pins</b> — pin one plugin to a cached version per agent</summary>

`beadle plugins pin <key> <version> --agent <id>`. The pin travels in the
vault; the farm, MCP paths and `heal` follow it, and a version missing from the
cache is reported instead of silently upgraded. *Why:* an upgrade can break a
plugin's skills or MCP servers mid-task.

</details>

<details>
<summary><b>Native bundles and hooks</b> — approved lifecycle hooks and the canon rendered into host plugins</summary>

A Claude marketplace plugin, a Gemini extension, an Antigravity plugin; the
bundle version follows the rendered bytes. A full forward sync makes one
unattended attempt per host that was never touched: render → validate →
register → probe → flip the file kinds off. Registration goes through the host
CLIs when they exist, and the result is verified before beadle retires the file
copies; without the CLI the canon keeps arriving as files and the registration
command is printed (`unverifiable`). A failed or unverified attempt is not
retried on every sync — the retry is explicit, `beadle bundles enable <host>`,
and a rendered canon change reopens it once; a host you disabled by hand stays
disabled until you enable it again. `bundles disable` materializes everything
back. Beadle writes hook files but never runs them. Cursor and Codex have no
bundle host: their approved hooks render into the user-level
`~/.cursor/hooks.json` and `~/.codex/hooks.json` instead, foreign hooks are
kept, and Codex asks you to review new hooks in `/hooks` before they run.
*Why:* native plugins survive upgrades and keep hooks in one audited place.

</details>

<details>
<summary><b>Agent Plugins export</b> — the canon as a portable Agent Plugins v1.0.0 package (skills + MCP)</summary>

`beadle export agent-plugins --out DIR` renders the canon into the portable
format: a closed-schema `plugin.json`, the skill trees (`skills/<name>/**`;
discovery is the immediate child with a `SKILL.md`, the rest of the tree — scripts,
references — is copied along), and `mcp.json` with its `$schema`/`mcpServers`
wrapper and the closed transport union (`stdio`, `streamable-http`, `sse`).
Servers are portable only when they stay inside the package: `${PLUGIN_ROOT}`
and relative references (in `command`, `args`, `env`, including behind spaces,
flags or `=`) must not climb above the root, and `command` must be a bare name
or a `./relative` path — it is never interpolated. `${VAR}` secrets are kept as
references, never expanded, and a client-side reference in `url`/`headers`/`env`
draws a warning: portable clients do not expand it, so that authentication stays
the client's own. The render is deterministic and idempotent, beadle-owned stale
files are removed on re-export, and a foreign file in `DIR` is never touched.
Commands, hooks, agents, rules and plugin-sourced components are outside the v1
pilot. *Why:* one conformant package a portable host can install, without
handing it your secrets.

</details>

<details>
<summary><b>Project scope</b> — per repository; <code>init</code> enables the present files, secrets stay out</summary>

[Project scope](#project-scope) names the files and the commands.

</details>

<details>
<summary><b>Claude memory</b> — per-project notes sync through the vault</summary>

A budget-capped digest lands in project files for the other agents; other
agents' inbox lines become notes; a secret gate runs before anything is
adopted. Codex, Pi and Kilo (like OpenCode and Cursor) can drop free-form
lines into their own `inbox.md`, and a sync turns them into project memory
notes. *Why:* only one agent remembers your project, and everyone else should
benefit.

</details>

<details>
<summary><b>Permissions</b> (default on) — tool, shell and MCP rules as one set, merged with <code>deny &gt; ask &gt; allow</code></summary>

Agent-local defaults and path globs stay local; keys must be canonical
(`tool:` names lowercase), and `doctor` warns about keys no codec can apply.
Since config v3 the permissions kind is synchronized by default; opt out with
`beadle kinds disable permissions`. *Why:* permission policy is currently
retyped in every agent.

</details>

<details>
<summary><b>Diagnostics and recovery</b> — see and undo what a sync did</summary>

`doctor` reports broken links, drift, collisions and half-finished states;
`conflicts`/`resolve` settle disagreements; `history`/`restore` roll kinds
back; `heal` clears quarantined plugins.

</details>

<details>
<summary><b>Git mergetool audit mode</b> (opt-in) — a conflict materialized as a real git merge state</summary>

The three sides become git objects under
`refs/beadle/mergetool/<id>/{base,vault,local}`, kept forever, and the commit
graph becomes the audit trail. File-backed kinds only. Details:
[ARCHITECTURE.md](ARCHITECTURE.md#mergetool-audit-mode).

</details>

<details>
<summary><b>Conflict contract</b> — a stable, secret-redacted view and a decision fingerprint</summary>

`beadle conflicts --json` shows the three sides plus a unified patch;
`resolve --from/--stdin` refuses a decision read before the conflict changed
(`stale-conflict`), and risky changes need `--allow-risky`. Details:
[ARCHITECTURE.md](ARCHITECTURE.md#conflict-contract).

</details>

## Commands

### Core

| Command | Purpose |
|---|---|
| `init [--agents a,b] [--no-daemon] [--daemon]` | create the vault, enable detected agents, wire the defaults (bundles, project files, watcher) |
| `sync [--dry-run] [--kind k]` | full cycle: pull, merge, push |
| `pull` / `push` | one direction only (`push` overwrites local agent edits) |
| `status [--check]` / `diff [--agent id]` | agents, modes and conflicts / what a sync would change |

### Conflicts & recovery

| Command | Purpose |
|---|---|
| `conflicts [id]` | open conflicts; with an id, the variants |
| `resolve [id…] --take vault\|agent\|file` | settle conflicts and push the decision |
| `history <kind>` / `restore <kind> --to N` | snapshots and rollbacks |
| `doctor` | diagnostics; non-zero exit on errors |
| `heal [--dry-run]` | clear quarantined plugins, stubs, tombstones and orphan pivots |

### Config & background

| Command | Purpose |
|---|---|
| `agents` (`enable`/`disable`/`mode`) | agents and their per-kind modes |
| `kinds` | switch kinds on or off for every agent |
| `watch` / `daemon` | background sync, autostart service |
| `secrets list\|set\|rm\|prune\|migrate` | credential values; never printed |
| `plugins pins\|pin\|unpin` | per-agent plugin version pins |
| `export agent-plugins --out DIR` | render the canon (skills + MCP) as an Agent Plugins v1.0.0 package |
| `hooks list\|add\|rm\|approve\|revoke` | lifecycle hooks canon: rendered into native bundles and the Cursor/Codex hooks files (never executed by beadle); `approve --plugin <key>` adopts the command hooks of an installed plugin |
| `skills seed\|adopt\|unadopt` | the built-in skill, foreign-copy adoption and its restore |
| `explain <skill>` | which copy of a canon skill each host can read |
| `bundles status\|enable [host]\|disable [host]` | native host bundles and their registration |
| `rulings list\|show\|trust\|forget` | remembered conflict decisions applied only on an exact, non-blast-radius match |
| `project status\|enable <file>\|disable <file>\|forget <file>` | per-repository project scope; nothing happens without an enabled policy |

Global flags: `--vault` (default `~/.beadle`, or `$BEADLE_HOME`), `--verbose`,
`--log-json`.

## Project scope

`beadle init` in a git checkout enables the project files that are present on
disk, with secrets kept out; `beadle project enable <file>` records a file
manually, and `beadle project status|disable|forget` manages the policy.
Without a policy (or outside a git checkout) nothing in a repository is
touched; `doctor` keeps reporting foreign `.mcp.json` / `projects.*`
collisions. Secret values materialize only in gitignored files; a git-tracked
file needs the explicit per-file opt-in (`beadle project enable <file>
--allow-secrets`).

The files are `.mcp.json`, `.claude/rules/*.md`, `.cursor/mcp.json`,
`.cursor/rules/*.mdc`, `.agents/mcp_config.json`, `AGENTS.md` and `GEMINI.md`. Project identity comes
from the git origin, so clones and worktrees converge; a missing file is never
treated as a deletion — `project forget` removes scope, a sync never does.
*Why:* the config that matters most is per-repo, and repos are where a
personal vault can leak.

## Secrets

The gate extracts credential values out of the canon into `mcp/secrets.json`
(0600, never committed) or the OS keychain (`secrets migrate keyring`); files
carry `{secret:NAME}` references, and shareable files render `${NAME}` instead
of a value. The scope (MCP, memory and project files; rules, skills and
permissions are not scanned) is described under [Reference](#reference). With
`backend: keyring` the store is fail-closed: an unavailable keyring yields
skips and warnings, never a plaintext fallback.

## Principles

- **Real files, symlinks respected.** Beadle never creates symlinks and never
  replaces yours: a write goes through a link to its target; broken links are
  reported by `doctor`. Project files are stricter — a symlink, a hard link or
  a non-regular file is refused.
- **Nothing is deleted silently.** Deletions propagate only relative to each
  agent's own base, mass deletions wait as conflicts, and project files that
  disappear (a branch switch, `git clean`) are reported as `kept`, not removed.
- **Atomic writes.** tmp file + fsync + rename in the target directory, then
  fsync of the directory; the file mode is preserved.
- **Local stays local.** OAuth state, hooks, plugins, UI settings, folder trust
  — untouched. Secrets never reach the synced canon, the object store or git
  history; the keyring backend is fail-closed — skips and warnings, never a
  plaintext fallback.

<details>
<summary><b>Development</b> — build, test and the package layout</summary>

```bash
make test    # go test -race ./...
make lint    # golangci-lint
make fmt
make mod     # go mod tidy && go mod vendor (deps are vendored)
```

Layout: `cmd/beadle`,
`pkg/{cli,vault,config,state,cas,fsutil,merge,kind,agent,engine,project,lock,watch,daemon,history,secret,mcp,skill,permission,subagent,command,frontmatter,bundle,digest,hooks,inbox,plugin,rulings,skills}`.

</details>

## License

MIT.
