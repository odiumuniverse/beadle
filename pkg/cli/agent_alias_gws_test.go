package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/state"
)

// gwsLoadConfig reads the vault config back after a command wrote it.
func gwsLoadConfig(t *testing.T, home string) *config.Config {
	t.Helper()

	cfg, err := config.Load(filepath.Join(home, ".beadle", config.FileName))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	return cfg
}

func TestInitAcceptsHistoricalAgentIDs(t *testing.T) {
	Convey("Given an empty home", t, func() {
		home := gwsHome(t)

		Convey("When init selects the agents with a historical id", func() {
			out, err := gwsRun(t, "init", "--agents", "claude-code,gemini-cli,deepseek-harness")

			Convey("Then it enables the canonical ids", func() {
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "[x]")

				cfg := gwsLoadConfig(t, home)

				for _, id := range []string{"claude", "gemini", "dsh"} {
					Convey("Then "+id+" is enabled", func() {
						So(cfg.Agents[id].Enabled, ShouldBeTrue)
					})
				}

				Convey("And no historical id is left in the config", func() {
					So(cfg.Agents, ShouldNotContainKey, "claude-code")
					So(cfg.Agents, ShouldNotContainKey, "gemini-cli")
					So(cfg.Agents, ShouldNotContainKey, "deepseek-harness")
				})
			})
		})
	})
}

func TestAgentsToggleAcceptsHistoricalIDs(t *testing.T) {
	Convey("Given an initialized vault", t, func() {
		home := gwsHome(t)

		if _, err := gwsRun(t, "init"); err != nil {
			t.Fatal(err)
		}

		Convey("When enable and disable are given a historical id", func() {
			_, err := gwsRun(t, "agents", "enable", "claude-code")
			So(err, ShouldBeNil)

			_, err = gwsRun(t, "agents", "disable", "gemini-cli")
			So(err, ShouldBeNil)

			Convey("Then the canonical keys carry the change", func() {
				cfg := gwsLoadConfig(t, home)
				So(cfg.Agents["claude"].Enabled, ShouldBeTrue)
				So(cfg.Agents["gemini"].Enabled, ShouldBeFalse)
				So(cfg.Agents, ShouldNotContainKey, "claude-code")
				So(cfg.Agents, ShouldNotContainKey, "gemini-cli")
			})
		})
	})
}

func TestAgentsModeAcceptsHistoricalID(t *testing.T) {
	Convey("Given an initialized vault", t, func() {
		home := gwsHome(t)

		if _, err := gwsRun(t, "init"); err != nil {
			t.Fatal(err)
		}

		Convey("When the mode is set with a historical id", func() {
			out, err := gwsRun(t, "agents", "mode", "claude-code", "mcp", "off")
			So(err, ShouldBeNil)

			Convey("Then it reports the canonical id and stores the mode under it", func() {
				So(out, ShouldContainSubstring, "claude mcp: off")

				cfg := gwsLoadConfig(t, home)
				So(cfg.Agents["claude"].Modes[kind.MCP], ShouldEqual, config.ModeOff)
			})
		})
	})
}

func TestPluginPinsAcceptHistoricalAgentID(t *testing.T) {
	Convey("Given an initialized vault", t, func() {
		home := gwsHome(t)

		if _, err := gwsRun(t, "init"); err != nil {
			t.Fatal(err)
		}

		Convey("When a pin is set with a historical id", func() {
			out, err := gwsRun(t, "plugins", "pin", "acme/tool", "1.0.0", "--agent", "claude-code")
			So(err, ShouldBeNil)
			So(out, ShouldContainSubstring, "claude")

			cfg := gwsLoadConfig(t, home)
			So(cfg.Agents["claude"].PluginPins["acme/tool"], ShouldEqual, "1.0.0")

			Convey("And the list filtered by the alias shows the pin", func() {
				out, err := gwsRun(t, "plugins", "pins", "--agent", "claude-code")
				So(err, ShouldBeNil)
				So(out, ShouldContainSubstring, "acme/tool")
			})

			Convey("And unsetting through the alias removes the canonical entry", func() {
				_, err := gwsRun(t, "plugins", "unpin", "acme/tool", "--agent", "claude-code")
				So(err, ShouldBeNil)

				cfg := gwsLoadConfig(t, home)
				So(cfg.Agents["claude"].PluginPins, ShouldNotContainKey, "acme/tool")
			})
		})
	})
}

func TestDiffAcceptsHistoricalAgentID(t *testing.T) {
	Convey("Given a vault with one open conflict", t, func() {
		home := gwsHome(t)
		gwsRules(t, home, "# claude\n", "# opencode\n")
		gwsInitSync(t)

		Convey("When diff filters by a historical id and by the canonical id", func() {
			byAlias, err := gwsRun(t, "diff", "--agent", "claude-code")
			So(err, ShouldBeNil)

			byCanonical, err := gwsRun(t, "diff", "--agent", "claude")
			So(err, ShouldBeNil)

			Convey("Then both select the same changes", func() {
				So(byAlias, ShouldNotBeEmpty)
				So(byAlias, ShouldEqual, byCanonical)
			})
		})
	})
}

func TestResolveAcceptsHistoricalAgentID(t *testing.T) {
	Convey("Given a vault with an open conflict on one agent", t, func() {
		home := gwsHome(t)
		gwsWrite(t, filepath.Join(home, ".claude", "CLAUDE.md"), "# claude\n")
		gwsWrite(t, filepath.Join(home, ".gemini", "GEMINI.md"), "# gemini\n")

		gwsInitSync(t)

		Convey("When resolve selects the agent by its historical id", func() {
			_, err := gwsRun(t, "resolve", "--all", "--agent", "gemini-cli", "--take", "vault")
			So(err, ShouldBeNil)

			out, err := gwsRun(t, "conflicts", "--json")
			So(err, ShouldBeNil)

			Convey("Then that conflict is gone", func() {
				So(out, ShouldContainSubstring, `"conflicts": []`)
			})
		})
	})
}

// TestMigratedConflictSurvivesAndResolves proves the one thing the conflict-file
// rename rests on: an unresolved conflict of a v3 vault keeps its meaning
// across the migration and is still settleable under the new agent id. The
// conflict file itself is not migrated — its name carries a hash of the agent
// id — so this is what proves no conflict is lost on the way.
func TestMigratedConflictSurvivesAndResolves(t *testing.T) {
	Convey("Given a v3 vault holding one unresolved conflict", t, func() {
		home := gwsHome(t)
		gwsWrite(t, filepath.Join(home, ".claude", "CLAUDE.md"), "# claude\n")
		gwsWrite(t, filepath.Join(home, ".gemini", "GEMINI.md"), "# gemini\n")

		gwsInitSync(t)

		before := gwsConflictViews(t, "conflicts", "--json")
		So(before.Conflicts, ShouldHaveLength, 1)
		So(before.Conflicts[0].Agent, ShouldEqual, "gemini")

		oldFile := gwsDowngradeToV3(t, home, "gemini-cli")
		So(gwsRead(t, oldFile), ShouldNotBeEmpty)

		Convey("When a sync migrates the vault", func() {
			// The migration itself succeeded; the vault it migrated has a
			// conflict in it, and a sync that leaves one open exits with the
			// conflict class rather than success.
			out, err := gwsRun(t, "sync")
			So(gwsBlocked(err), ShouldBeTrue)
			So(out, ShouldContainSubstring, "state: agent gemini-cli is now named gemini")

			Convey("Then the conflict survives under the new id", func() {
				after := gwsConflictViews(t, "conflicts", "--json")
				So(after.Conflicts, ShouldHaveLength, 1)
				So(after.Conflicts[0].Agent, ShouldEqual, "gemini")
				So(string(after.Conflicts[0].Kind), ShouldEqual, "rules")
				So(after.Conflicts[0].Key, ShouldEqual, "main")
				So(after.Conflicts[0].File, ShouldNotBeEmpty)
			})

			Convey("And the conflict file is written under the new name", func() {
				So(gwsRead(t, afterConflictFile(t, home)), ShouldNotBeEmpty)

				_, err := os.Stat(oldFile)
				So(os.IsNotExist(err), ShouldBeTrue)
			})

			Convey("And diff reaches it through the historical id", func() {
				byAlias, err := gwsRun(t, "diff", "--agent", "gemini-cli")
				So(err, ShouldBeNil)

				byCanonical, err := gwsRun(t, "diff", "--agent", "gemini")
				So(err, ShouldBeNil)

				So(byAlias, ShouldEqual, byCanonical)
				So(byAlias, ShouldContainSubstring, afterConflictID(t, home))
			})

			Convey("And resolve settles it through the historical id", func() {
				_, err := gwsRun(t, "resolve", "--all", "--agent", "gemini-cli", "--take", "vault")
				So(err, ShouldBeNil)

				Convey("Then no conflict is left and the host file took the vault value", func() {
					So(gwsConflictViews(t, "conflicts", "--json").Conflicts, ShouldBeEmpty)
					So(gwsRead(t, filepath.Join(home, ".gemini", "GEMINI.md")), ShouldEqual, "# claude\n")
				})
			})
		})
	})
}

// gwsDowngradeToV3 rewrites a migrated vault back into the documents a v3
// build would have left: the same content under the old schema version, the old
// agent key, the old agent id in the open conflict, and the conflict file under
// the name that old id hashed to. It returns that old conflict file path.
func gwsDowngradeToV3(t *testing.T, home, oldID string) string {
	t.Helper()

	vault := filepath.Join(home, ".beadle")

	configPath := filepath.Join(vault, config.FileName)

	var cfg map[string]any
	if err := json.Unmarshal([]byte(gwsRead(t, configPath)), &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}

	cfg["version"] = 3

	agents, ok := cfg["agents"].(map[string]any)
	So(ok, ShouldBeTrue)

	agents[oldID] = agents["gemini"]
	delete(agents, "gemini")

	raw, err := json.MarshalIndent(cfg, "", "  ")
	So(err, ShouldBeNil)
	gwsWrite(t, configPath, string(raw))

	statePath := filepath.Join(vault, state.FileName)

	var st map[string]any
	if err := json.Unmarshal([]byte(gwsRead(t, statePath)), &st); err != nil {
		t.Fatalf("parse state: %v", err)
	}

	st["version"] = 2

	conflicts, ok := st["conflicts"].([]any)
	So(ok, ShouldBeTrue)
	So(conflicts, ShouldHaveLength, 1)

	conflict, ok := conflicts[0].(map[string]any)
	So(ok, ShouldBeTrue)

	conflictKind, ok := conflict["kind"].(string)
	So(ok, ShouldBeTrue)

	conflictKey, ok := conflict["key"].(string)
	So(ok, ShouldBeTrue)

	conflict["agent"] = oldID

	raw, err = json.MarshalIndent(st, "", "  ")
	So(err, ShouldBeNil)
	gwsWrite(t, statePath, string(raw))

	// A v3 build named the file after a hash of the OLD agent id, so the
	// migration has to collect that name and write the new one.
	oldConflict := state.Conflict{Kind: kind.ID(conflictKind), Agent: oldID, Key: conflictKey}
	oldName := fmt.Sprintf("%s-%s-%s%s", conflictKind, oldID, oldConflict.ID(), filepath.Ext(afterConflictFile(t, home)))

	from := afterConflictFile(t, home)
	to := filepath.Join(filepath.Dir(from), oldName)

	So(os.Rename(from, to), ShouldBeNil)

	return to
}

// afterConflictFile returns the single conflict file of the vault.
func afterConflictFile(t *testing.T, home string) string {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join(home, ".beadle", "conflicts"))
	if err != nil {
		t.Fatalf("read conflicts directory: %v", err)
	}

	So(entries, ShouldHaveLength, 1)

	return filepath.Join(home, ".beadle", "conflicts", entries[0].Name())
}

// afterConflictID returns the id of the single open conflict.
func afterConflictID(t *testing.T, home string) string {
	t.Helper()

	views := gwsConflictViews(t, "conflicts", "--json")
	So(views.Conflicts, ShouldHaveLength, 1)

	return views.Conflicts[0].ID
}
