package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/state"
	"github.com/odiumuniverse/beadle/pkg/testhost"
)

// v2State is a state.json in the shape the build that still used the historical
// agent ids actually wrote: per-agent bases keyed by the old id, and the
// conflict, refusal and adoption records that name an agent by it.
//
// It carries more than the rename touches on purpose. `migrate` is the command
// that persists a state upgrade, and a state file is the only record of what
// the last sync saw — a migrate that rewrote the ids and dropped the rest
// would leave a vault that syncs cleanly against nothing. Every block below is
// something a user would be upset to lose, and each one is asserted on the
// bytes that reached the disk.
//
// The version is 2, below state.CurrentVersion: a file at or above the current
// version is either a no-op or an unsupported-state error, and neither is the
// upgrade path under test.
const v2State = `{
  "version": 2,
  "bases": {
    "mcp": {"claude-code": {"alpha": "h1"}, "opencode": {"alpha": "h2"}},
    "skills": {"gemini-cli": {"beta": "h3"}, "deepseek-harness": {"gamma": "h4"}}
  },
  "conflicts": [
    {"kind": "mcp", "agent": "claude-code", "key": "alpha", "reason": "modified", "base": "h0", "vault": "h1", "local": "h2", "since": "2026-01-01T00:00:00Z"},
    {"kind": "skills", "agent": "gemini-cli", "key": "beta", "reason": "deleted", "since": "2026-01-02T00:00:00Z"}
  ],
  "snapshots": {
    "mcp": [{"at": "2026-01-01T00:00:00Z", "manifest": "hm"}]
  },
  "renders": {
    "/home/u/p/CLAUDE.md": {"block_hash": "hr", "inputs_hash": "hi", "notes": 3, "at": "2026-01-03T00:00:00Z"}
  },
  "drift": {
    "/home/u/p/CLAUDE.md": {"count": 2, "first": "2026-01-01T00:00:00Z", "last": "2026-01-04T00:00:00Z"}
  },
  "bundles": {
    "claude": {
      "enabled": true,
      "version": "1.4.0",
      "registered": true,
      "verify_tier": "executed",
      "withdrawn": [
        {"kind": "skills", "name": "alpha", "digest": "hwd1", "at": "2026-01-05T00:00:00Z"},
        {"kind": "mcp", "name": "beta", "at": "2026-01-06T00:00:00Z"}
      ],
      "saved_modes": {"permissions": "off"},
      "auto_attempt": {"version": "1.4.0", "at": "2026-01-07T00:00:00Z", "tier": "executed"},
      "pending": {"version": "1.5.0", "since": "2026-01-08T00:00:00Z"},
      "complement": {"mcp": ["acme/tool"]}
    },
    "gemini": {"enabled": false}
  },
  "refusals": [
    {"at": "2026-01-01T00:00:00Z", "id": "abc", "kind": "mcp", "agent": "antigravity-cli", "key": "gamma", "code": "risky-change", "message": "no"}
  ],
  "adoptions": [
    {"host": "claude-code", "name": "alpha", "provider": "/home/u/.claude/skills/alpha", "digest": "had", "at": "2026-01-01T00:00:00Z"},
    {"host": "deepseek-harness", "name": "delta", "provider": "/home/u/.dsh/skills/delta", "at": "2026-01-02T00:00:00Z"}
  ],
  "skill_trees": {
    "/home/u/.claude/skills": {"digest": "hst", "fingerprint": "hsfp", "latest": "2026-01-09T00:00:00Z", "stamp": "2026-01-09T00:01:00Z"}
  },
  "hook_renders": {
    "claude": ["SessionStart"]
  },
  "hook_modules": {
    "/home/u/.claude/plugins/hooks/start.js": {"digest": "hhm", "source": "acme/tool", "phase": "start", "name": "start.js"}
  },
  "bundle_opt_out": ["gemini"],
  "home": "/home/u"
}
`

// TestMigrateUpgradesAnOldStateJSONOnDisk covers the half of `beadle migrate`
// nothing had ever run: state.json. The config half had a test; the state half
// had none, so a `migrateState` that returned early, saved to the wrong path,
// or saved a state the migration had emptied would have passed the suite.
//
// Every assertion here reads the bytes on disk. `state.Load` migrates in
// memory and reports the current version whether or not the command ever saved,
// so a re-read through it would be satisfied by the migration alone: the file
// under test would never be consulted. The decoded maps come from
// encoding/json, not from any beadle type, so the assertions describe what the
// bytes say and nothing else.
// writeOldStateJSON lays a state.json from an older build over the vault.
//
// The sanity check belongs here rather than inline so the fixture's own version
// cannot drift: a fixture that is already current would turn every assertion in
// the test into a check of the no-op path, and the test would still be green.
func writeOldStateJSON(t *testing.T, path string) {
	t.Helper()

	So(strings.Contains(v2State, fmt.Sprintf(`"version": %d`, state.CurrentVersion-1)), ShouldBeTrue)
	So(state.CurrentVersion, ShouldBeGreaterThan, 1)
	So(os.WriteFile(path, []byte(v2State), 0o600), ShouldBeNil)
}

// assertMigratedStateIsIntact is every per-item check the migration has to
// survive, in one place: the record a user would be upset to lose, compared
// against a full expected structure rather than a substring. It is a
// function and not more inline Convey blocks only because the list is long
// and reads as a checklist, not as a sequence of steps.
func assertMigratedStateIsIntact(onDisk map[string]any, raw string) {
	Convey("And every per-agent base is keyed by its canonical id, hashes intact", func() {
		bases, ok := onDisk["bases"].(map[string]any)
		So(ok, ShouldBeTrue)

		So(bases["mcp"], ShouldResemble, map[string]any{
			"claude":   map[string]any{"alpha": "h1"},
			"opencode": map[string]any{"alpha": "h2"},
		})
		So(bases["skills"], ShouldResemble, map[string]any{
			"gemini": map[string]any{"beta": "h3"},
			"dsh":    map[string]any{"gamma": "h4"},
		})

		// The point of the rename is that the base moves with the
		// agent, so the digests have to travel with the keys.
		So(raw, ShouldNotContainSubstring, "claude-code")
		So(raw, ShouldNotContainSubstring, "gemini-cli")
		So(raw, ShouldNotContainSubstring, "deepseek-harness")
		So(raw, ShouldNotContainSubstring, "antigravity-cli")
	})

	Convey("And every conflict keeps its verdict, hashes and reason", func() {
		So(onDisk["conflicts"], ShouldResemble, []any{
			map[string]any{
				"kind": "mcp", "agent": "claude", "key": "alpha",
				"reason": "modified", "base": "h0", "vault": "h1", "local": "h2",
				"since": "2026-01-01T00:00:00Z",
			},
			map[string]any{
				"kind": "skills", "agent": "gemini", "key": "beta",
				"reason": "deleted", "since": "2026-01-02T00:00:00Z",
			},
		})
	})

	Convey("And the refusal journal survives with its agent renamed", func() {
		So(onDisk["refusals"], ShouldResemble, []any{
			map[string]any{
				"at": "2026-01-01T00:00:00Z", "id": "abc", "kind": "mcp",
				"agent": "agy", "key": "gamma", "code": "risky-change", "message": "no",
			},
		})
	})

	Convey("And every adopted skill keeps its stash record under the canonical host", func() {
		So(onDisk["adoptions"], ShouldResemble, []any{
			map[string]any{
				"host": "claude", "name": "alpha",
				"provider": "/home/u/.claude/skills/alpha",
				"digest":   "had", "at": "2026-01-01T00:00:00Z",
			},
			map[string]any{
				"host": "dsh", "name": "delta",
				"provider": "/home/u/.dsh/skills/delta", "at": "2026-01-02T00:00:00Z",
			},
		})
	})

	Convey("And the bundle state, withdrawn items and pins are all still there", func() {
		bundles, ok := onDisk["bundles"].(map[string]any)
		So(ok, ShouldBeTrue)

		claude, ok := bundles["claude"].(map[string]any)
		So(ok, ShouldBeTrue)

		// Every field a disable has to read back to put the canon
		// element where the bundle took it out of.
		So(claude["enabled"], ShouldEqual, true)
		So(claude["version"], ShouldEqual, "1.4.0")
		So(claude["registered"], ShouldEqual, true)
		So(claude["verify_tier"], ShouldEqual, "executed")
		So(claude["withdrawn"], ShouldResemble, []any{
			map[string]any{
				"kind": "skills", "name": "alpha", "digest": "hwd1",
				"at": "2026-01-05T00:00:00Z",
			},
			map[string]any{
				"kind": "mcp", "name": "beta", "at": "2026-01-06T00:00:00Z",
			},
		})
		So(claude["saved_modes"], ShouldResemble, map[string]any{"permissions": "off"})
		So(claude["auto_attempt"], ShouldResemble, map[string]any{
			"version": "1.4.0", "at": "2026-01-07T00:00:00Z", "tier": "executed",
		})
		So(claude["pending"], ShouldResemble, map[string]any{
			"version": "1.5.0", "since": "2026-01-08T00:00:00Z",
		})
		So(claude["complement"], ShouldResemble, map[string]any{"mcp": []any{"acme/tool"}})
		So(bundles["gemini"], ShouldResemble, map[string]any{"enabled": false})
	})

	Convey("And the rest of the record is untouched", func() {
		So(onDisk["snapshots"], ShouldResemble, map[string]any{
			"mcp": []any{map[string]any{"at": "2026-01-01T00:00:00Z", "manifest": "hm"}},
		})
		So(onDisk["renders"], ShouldResemble, map[string]any{
			"/home/u/p/CLAUDE.md": map[string]any{
				"block_hash": "hr", "inputs_hash": "hi", "notes": float64(3),
				"at": "2026-01-03T00:00:00Z",
			},
		})
		So(onDisk["drift"], ShouldResemble, map[string]any{
			"/home/u/p/CLAUDE.md": map[string]any{
				"count": float64(2), "first": "2026-01-01T00:00:00Z", "last": "2026-01-04T00:00:00Z",
			},
		})
		// The cache key is the tree spelled relative to the recorded home, not
		// the machine's path: a vault that travels to a machine with another home
		// would otherwise carry a key naming nothing there. The digest, the
		// fingerprint and both timestamps travel with it untouched - only the name
		// is rewritten.
		So(onDisk["skill_trees"], ShouldResemble, map[string]any{
			state.HomePrefix + ".claude/skills": map[string]any{
				"digest": "hst", "fingerprint": "hsfp",
				"latest": "2026-01-09T00:00:00Z", "stamp": "2026-01-09T00:01:00Z",
			},
		})
		// Hook renders are keyed by the host they went into, never by the path of
		// the file: the host is the identity that travels, and a path is not. The
		// fixture says so, and the migration leaves the shape alone.
		So(onDisk["hook_renders"], ShouldResemble, map[string]any{
			"claude": []any{"SessionStart"},
		})
		// A hook module is keyed by the machine-independent name of the file it
		// wrote, for the reason the cache is: the record is the only proof beadle
		// has that the file is its own, and a proof naming another machine's home
		// cannot survive there. The record's claim travels whole.
		So(onDisk["hook_modules"], ShouldResemble, map[string]any{
			state.HomePrefix + ".claude/plugins/hooks/start.js": map[string]any{
				"digest": "hhm", "source": "acme/tool", "phase": "start", "name": "start.js",
			},
		})
		So(onDisk["bundle_opt_out"], ShouldResemble, []any{"gemini"})
		So(onDisk["home"], ShouldEqual, "/home/u")
	})
}

func TestMigrateUpgradesAnOldStateJSONOnDisk(t *testing.T) {
	Convey("Given a vault whose state.json is still at v2", t, func() {
		home := gwsHome(t)
		So(os.MkdirAll(filepath.Join(home, ".claude"), 0o700), ShouldBeNil)

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		vaultDir := filepath.Join(home, ".beadle")
		statePath := filepath.Join(vaultDir, state.FileName)

		writeOldStateJSON(t, statePath)

		Convey("When migrate runs", func() {
			out, migrateErr := runCLI(t, "migrate")
			So(migrateErr, ShouldBeNil)
			So(out, ShouldNotContainSubstring, "nothing to migrate")

			raw := readFile(t, statePath)
			onDisk := map[string]any{}
			So(json.Unmarshal([]byte(raw), &onDisk), ShouldBeNil)

			assertMigratedStateIsIntact(onDisk, raw)

			Convey("Then the state on disk is the current version", func() {
				So(onDisk["version"], ShouldEqual, float64(state.CurrentVersion))
			})

			Convey("And the output names each rename it made", func() {
				So(out, ShouldContainSubstring, "state: agent claude-code is now named claude")
				So(out, ShouldContainSubstring, "state: agent gemini-cli is now named gemini")
				So(out, ShouldContainSubstring, "state: agent deepseek-harness is now named dsh")
				So(out, ShouldContainSubstring, "state: agent antigravity-cli is now named agy")
			})

			Convey("And a second run writes nothing at all", func() {
				before := snapshotTree(t, vaultDir)

				out, rerunErr := runCLI(t, "migrate")
				So(rerunErr, ShouldBeNil)
				So(out, ShouldContainSubstring, "the vault is current; nothing to migrate")

				// Not just the state: the vault is byte-identical, which is
				// the property a re-run on a timer depends on.
				So(snapshotTree(t, vaultDir), ShouldResemble, before)
				So(readFile(t, statePath), ShouldEqual, raw)
			})
		})
	})
}

// vaultManifest reproduces the gate's own tree hash: every regular file under
// dir, each one's sha256, the lines sorted, and the sha256 of that listing. The
// gate builds it with
//
//	find .beadle -type f -exec shasum {} \; | sort | shasum
//
// and it is that shape, not a single file, that makes the check mean "the vault
// did not move": a re-run that rewrote one file inside a directory of forty
// still changes this value, and a re-run that added or removed a file does too.
//
// The paths are relative to dir, so the value does not depend on where the temp
// home happened to land.
func vaultManifest(t *testing.T, dir string) string {
	t.Helper()

	var lines []string

	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			return nil
		}

		data, readErr := os.ReadFile(path) //nolint:gosec // G304: a test reads its own temp vault
		if readErr != nil {
			return readErr
		}

		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}

		sum := sha256.Sum256(data)
		lines = append(lines, hex.EncodeToString(sum[:])+"  "+filepath.ToSlash(rel))

		return nil
	})
	if err != nil {
		t.Fatalf("manifest %s: %v", dir, err)
	}

	slices.Sort(lines)

	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))

	return hex.EncodeToString(sum[:])
}

// gateLedger is the ledger the gate writes for itself after the two `claude
// plugin …` commands have run: beadle's own `marketplace/name` key form, the
// install path the real CLI reported, and `"source": "claude-code"`.
//
// The gate builds this file with python3 out of
// $CLAUDE_CONFIG_DIR/plugins/installed_plugins.json, and it writes no top-level
// "version" — which the reader treats as 0 and the writer then stamps. It is
// reproduced here verbatim, because the ledger is the state the gate reaches
// *through* those two commands: what they do to the vault is exactly this file.
const gateLedgerName = "claude-plugins-official/frontend-design"

func gateLedgerJSON(installPath string) string {
	return fmt.Sprintf(`{"plugins": {%q: {"version": "1.0.0", "sha": %q, "target": %q, "source": "claude-code", "updated_at": "2026-01-01T00:00:00Z"}}}`,
		gateLedgerName, strings.Repeat("0", 40), installPath)
}

// TestMigrateIdempotentInTheGateForm is the unit-test half of the gate's group
// 15 (`final-gate.sh:926-966`), reproduced in the gate's own order:
//
//	a third-party ~/.agents/.skill-lock.json, written before anything else
//	a plugin ledger in beadle's marketplace/name form
//	beadle init -y
//	beadle bundles enable --host claude
//	beadle sync
//	beadle sync
//	    hash every file under .beadle          <- before
//	beadle sync
//	    hash every file under .beadle          <- after
//
// and two requirements: the manifest is identical, and the third-party file is
// byte-identical. `migrate.idempotent` is the third sync.
//
// The boundary, stated plainly: the gate's first two commands are
// `claude plugin marketplace add anthropics/claude-plugins-official` and
// `claude plugin install frontend-design@claude-plugins-official` — the real
// Claude Code CLI against a real network, not beadle subcommands. A unit test
// must depend on neither, so this test does not run them by default; it writes
// the ledger the gate writes, which is the whole of the state those two commands
// leave in the vault. `BEADLE_LIVE_CLAUDE=1` plus a `claude` on PATH runs the
// real pair first and derives the ledger from the install record they produce,
// and the test then reports which half it ran.
func TestMigrateIdempotentInTheGateForm(t *testing.T) {
	Convey("Given a home shaped like the gate's group 15", t, func() {
		// The hosts are pinned, not inherited. Which agents exist is decided
		// by LookPath on their CLIs, so a test that lets PATH and HOME decide
		// measures the developer's machine: the same assertion then means one
		// thing on a box with the Claude Code CLI installed and another on a
		// Linux box with none, and the gate's own result is only reachable
		// where a host CLI exists. The stubs are inert — they exit 0 — so what
		// they contribute is which hosts exist, never what they answer.
		testhost.Stubs(t)

		home := gwsHome(t)
		installPath := filepath.Join(home, ".claude", "plugins", "marketplaces",
			"claude-plugins-official", "plugins", "frontend-design")
		writeFile(t, filepath.Join(installPath, "skills", "frontend", "SKILL.md"), "# frontend\n")
		writeFile(t, filepath.Join(installPath, ".claude-plugin", "plugin.json"), `{"name": "frontend-design"}`)

		claudeConfigDir := filepath.Join(home, ".claude")
		So(os.MkdirAll(claudeConfigDir, 0o700), ShouldBeNil)

		// The gate writes both of these before anything else runs, and the
		// one under .agents is the one it hashes.
		So(os.MkdirAll(filepath.Join(home, ".agents"), 0o700), ShouldBeNil)
		writeFile(t, filepath.Join(home, ".agents-skill-lock.json"), `{"lock":"third-party","v":1}`+"\n")

		foreignPath := filepath.Join(home, ".agents", ".skill-lock.json")
		writeFile(t, foreignPath, `{"lock":"third-party","v":1}`+"\n")
		foreignBefore := readFile(t, foreignPath)

		Convey("When the gate's own sequence runs", func() {
			Convey("Then the ledger is the gate's, and init, sync, sync all succeed", func() {
				So(os.MkdirAll(filepath.Join(home, ".beadle", "plugins"), 0o700), ShouldBeNil)

				ledger := gateLedgerJSON(installPath)
				if live := runRealClaudePluginInstall(t, home, claudeConfigDir); live != "" {
					ledger = live
				}

				So(os.WriteFile(filepath.Join(home, ".beadle", "plugins", "ledger.json"), []byte(ledger), 0o600), ShouldBeNil)

				_, err := runCLI(t, "init", "-y")
				So(err, ShouldBeNil)

				// On the gate's machine `claude` is a real CLI, so the
				// bundle probe reaches it and the bundle is registered. That
				// is what puts canon skills inside the vault's bundle tree,
				// and those are the skill trees the digest cache is keyed on —
				// the one part of a sync that used to re-stamp state.json from
				// the clock on every run. A fixture that leaves the bundle off
				// never reaches that code, and the idempotency assertion below
				// would hold vacuously.
				_, err = runCLI(t, "bundles", "enable", "--host", "claude")
				So(err, ShouldBeNil)

				_, err = runCLI(t, "sync")
				So(err, ShouldBeNil)

				_, err = runCLI(t, "sync")
				So(err, ShouldBeNil)

				vaultDir := filepath.Join(home, ".beadle")

				// The manifest is only a claim if it covers a real tree.
				So(len(snapshotTree(t, vaultDir)), ShouldBeGreaterThan, 1)

				before := vaultManifest(t, vaultDir)

				_, err = runCLI(t, "sync")
				So(err, ShouldBeNil)

				Convey("And the third sync leaves every file in the vault byte-identical", func() {
					So(vaultManifest(t, vaultDir), ShouldEqual, before)
				})

				Convey("And the third-party skill lock is byte-identical", func() {
					So(readFile(t, foreignPath), ShouldEqual, foreignBefore)
				})

				Convey("And the ledger the gate wrote is still the ledger, under its canonical source", func() {
					// The gate's ledger records source `claude-code`; the
					// id rename rewrites it on the first sync and leaves it
					// alone after that, which is half of why the third
					// sync is the one the gate hashes.
					onDisk := readFile(t, filepath.Join(vaultDir, "plugins", "ledger.json"))
					So(onDisk, ShouldContainSubstring, `"claude-plugins-official/frontend-design"`)
					So(onDisk, ShouldNotContainSubstring, "claude-code")
				})
			})
		})
	})
}

// runRealClaudePluginInstall runs the two `claude plugin …` commands the gate
// runs, and returns the ledger written from the install record they produce —
// the same derivation the gate does in python3, over the same
// installed_plugins.json. It returns "" when the real CLI is not being driven,
// which is the ordinary case: the commands need the network and a `claude`
// binary, and neither belongs in a unit test.
//
// Set BEADLE_LIVE_CLAUDE=1 to opt in. The test then fails on a real CLI that
// misbehaves, because a live run that silently degraded to the fixture would be
// reporting a pass it had not earned.
func runRealClaudePluginInstall(t *testing.T, home, configDir string) string {
	t.Helper()

	if os.Getenv("BEADLE_LIVE_CLAUDE") == "" {
		t.Log("BEADLE_LIVE_CLAUDE unset: the ledger is the gate's own fixture, " +
			"not one derived from a real `claude plugin install`")

		return ""
	}

	bin, err := exec.LookPath("claude")
	if err != nil {
		t.Fatalf("BEADLE_LIVE_CLAUDE is set but no claude is on PATH: %v", err)
	}

	run := func(args ...string) {
		t.Helper()

		//nolint:gosec // G204: running the beadle binary under test is the point
		cmd := exec.CommandContext(t.Context(), bin, args...)
		cmd.Env = []string{
			"HOME=" + home,
			"CLAUDE_CONFIG_DIR=" + configDir,
			"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
			"PATH=" + os.Getenv("PATH"),
		}

		if out, runErr := cmd.CombinedOutput(); runErr != nil {
			t.Fatalf("claude %s: %v: %s", strings.Join(args, " "), runErr, out)
		}
	}

	run("plugin", "marketplace", "add", "anthropics/claude-plugins-official")
	run("plugin", "install", "frontend-design@claude-plugins-official")

	t.Log("BEADLE_LIVE_CLAUDE=1: the ledger was derived from a real claude plugin install")

	// The gate's python3: first plugin, first record, the `name@market` key
	// split back into beadle's own marketplace/name form.
	installed := filepath.Join(configDir, "plugins", "installed_plugins.json")

	data, err := os.ReadFile(installed) //nolint:gosec // G304: the test read the CLI's own install record
	if err != nil {
		t.Fatalf("read %s: %v", installed, err)
	}

	var doc struct {
		Plugins map[string][]struct {
			Version      string `json:"version"`
			GitCommitSha string `json:"gitCommitSha"`
			InstallPath  string `json:"installPath"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse %s: %v", installed, err)
	}

	keys := slices.Sorted(maps.Keys(doc.Plugins))
	if len(keys) == 0 || len(doc.Plugins[keys[0]]) == 0 {
		t.Fatalf("claude installed no plugin: %s", installed)
	}

	name, marketplace, ok := strings.Cut(keys[0], "@")
	if !ok {
		t.Fatalf("unexpected installed_plugins.json key %q", keys[0])
	}

	rec := doc.Plugins[keys[0]][0]
	if rec.GitCommitSha == "" {
		rec.GitCommitSha = strings.Repeat("0", 40)
	}

	return fmt.Sprintf(`{"plugins": {%q: {"version": %q, "sha": %q, "target": %q, "source": "claude-code", "updated_at": "2026-01-01T00:00:00Z"}}}`,
		marketplace+"/"+name, rec.Version, rec.GitCommitSha, rec.InstallPath)
}
