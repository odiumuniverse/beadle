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
