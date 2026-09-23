# Architecture

How beadle moves configuration.

```
                     ~/.beadle — the vault (plain files you own)
       rules/ · mcp/ · skills/ · permissions/ · memory/ · projects/
       objects/ (content-addressed history) · state.json (sync state)
                             ▲                          │
                   pull      │                          │      push
                 agent → vault                    vault → agent
                             │                          ▼
   Claude Code · OpenCode · Gemini CLI · Cursor · Antigravity CLI · Codex CLI · Pi · Kilo Code
```

A sync pulls each agent's files into the vault, merges them against that
agent's base — the state the agent last saw, remembered by beadle and not
part of the canon — and pushes the union back. One canonical form with a
translation per agent; everything beadle does not manage is left alone.

```
   base ──┐
   vault ─┼──▶  3-way merge  ──▶  merged  ──▶  vault and every agent
   agent ─┘
```

If the vault and an agent both changed the same item, the merge cannot
decide: beadle records a conflict and keeps the item as it is. The conflict
waits for a human or an agent (`beadle conflicts`, `beadle resolve`) instead
of overwriting either side; the rest of the sync continues.

Credential values never travel in the canon: they are extracted into
`mcp/secrets.json` (0600, never committed) or the OS keychain, and files
carry `{secret:NAME}` references.

## Mergetool audit mode

Opt-in per conflict: `beadle resolve <id> --mergetool` materializes a text
conflict as a real git merge state inside the vault.

- The three sides become git objects under
  `refs/beadle/mergetool/<id>/{base,vault,local}` — kept forever, so the
  commit graph is the audit trail.
- The target path gets index stages 1/2/3, `MERGE_HEAD` and conflict markers,
  so the conflict can be settled with `git mergetool` or by hand.
- `beadle resolve <id> --mergetool-abort` removes the merge state; the audit
  refs stay.
- While the merge is active, `sync`/`diff`/`status`/`doctor` refuse with
  `mergetool-active`, so the marker file is never read as canon.
- Only file-backed kinds are supported. JSON kinds are refused with
  `mergetool-unsupported`, and applying the result still goes through the
  normal `--from`/`--take file` path.

## Conflict contract

`beadle conflicts --json` is a stable, secret-redacted view of an open
conflict: the three sides plus a unified patch. A decision carries the state it
was made against:

- `beadle resolve --from/--stdin` requires `--expect-base`, `--expect-vault`
  and `--expect-agent`; a decision read before the conflict changed is refused
  as `stale-conflict` instead of applied.
- Risky changes — an MCP `command`/`url`, a new server, any permission
  rule — need `--allow-risky`.
- `resolve --all` never touches permissions.

`beadle init` seeds the built-in skills — `beadle-conflicts` and the umbrella
`beadle` — into `<vault>/skills/` (for an existing vault: `beadle skills seed
[--force]`), so an agent can walk the loop — status → diff → `conflicts --json`
→ resolve → sync → doctor — through the same audited path a human uses:
resolving disagreements is exactly where an agent should not improvise.

## Formats are guests

`pkg/kind` defines how each resource merges; `pkg/agent` translates an agent's
own format and keeps everything it does not own. JSONC stays JSONC: comments
and formatting of untouched parts survive edits.
