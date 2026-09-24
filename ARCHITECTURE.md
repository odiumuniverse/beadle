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
   Claude Code · OpenCode · Gemini CLI · Cursor · Antigravity CLI · Codex CLI · Pi · Kilo Code · DeepSeek Harness
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

A host file whose canon item became inexpressible is kept; when its content
differs from the canon, `doctor` warns that the host still loads the stale copy
(remove the file or change the mode).

## Plugins are read from every host

A plugin installed in any supported agent — Claude Code, Codex, Gemini CLI
extensions, Antigravity, Cursor — is parked in the vault as
`plugins/<origin>/<name>/current`: a symlink into the host's own install
directory plus an ownership ledger that records the source host. The pivot
only accepts install paths from the roots of that host, and the farms resolve
the payload directories from the plugin manifest (compatibility layouts may
move them). Everything the plugin ships is presented to the other hosts:
skills and MCP servers, agents and commands (markdown links, Codex TOML
renders, Gemini TOML for the extension host and lifted markdown for the
others), and — only after an explicit `beadle hooks approve --plugin <key>` —
lifecycle hooks, read in the dialect of each host: nested matcher groups
(Claude, Codex, Gemini), Antigravity's owner-keyed file, and Cursor's flat
`{command, timeout?, matcher?}` entries.

A host keeps reading its **own** plugins natively: its agents, commands and
MCP servers are not presented back to it (only the skills farm keeps linking
next to the native copy), while every other host receives them. The same holds
when the plugin is installed in several hosts: the host whose copy lost the
dedup or the same-key source conflict keeps its native copy and receives
nothing from the winner — the ledger records those hosts in `overridden`.

The same plugin found in several hosts is presented once: equal artifact
digests (skill trees, agent and command bytes, hook commands, the MCP
document) make the copies one plugin, the first host in registration order
wins with a sync note, and divergent copies are warned about instead of being
silently hidden. The vault canon always outranks plugins: a canon skill with
the same name shadows the plugin one. Plugin payloads never enter the canon by
themselves.

## Bundles carry no secrets

A native bundle is a distributable package: everything inside is readable by
everyone who installs it. An MCP server whose configuration references a secret
(`{secret:…}`) therefore stays out of the rendered Claude, Gemini and
Antigravity bundles — a sync note and a doctor Info explain where it went —
and keeps arriving through the host MCP surface with resolved values. That
copy is refreshed by every sync while the MCP kind stays on; after a bundle
flip the kind is off, so the host copy is not updated until the bundle is
disabled (or the server leaves the filter). When a bundle that had withdrawn
such a server earlier is disabled, the server is resolved back into the host
config; a value that cannot resolve keeps the bundle enabled instead of
writing a dangling reference.

## State is a cache, not a source

`state.json` remembers per-agent bases, open conflicts, snapshots — and the
skill manifest cache (`skill_trees`): the digest of every skill tree beadle
scanned, keyed by its absolute root, together with the listing it was computed
from (relative path, size, modification time, permission bits) and the moment
of computation. A scan lists a tree (readdir + stat, no content reads) and
reuses the digest only while that listing matches and is older than the racy
window of the computation — a filesystem with a coarse clock can hide a
same-size change written in the same second; otherwise it reads the tree
again. The listing is the whole truth the cache trusts: a file rewritten with
the same size and a restored modification time (`touch -r`) is invisible to
it, and the cache then keeps the old digest while a cacheless scan would read
the new content. Read-only commands (`doctor`, `diff`, `status`, `explain`)
use the cache but never write it — the file changes only on the passes that
already save state (`sync`, `push`, bundles, adopt). Losing `state.json` costs
one full rescan, never a wrong decision for unchanged trees: the cache only
skips work, and its decisions match a cacheless scan whenever the listings
match.
