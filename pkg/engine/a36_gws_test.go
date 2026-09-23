package engine_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/state"
)

func a36CursorHooks(f *fixture) string { return filepath.Join(f.home, ".cursor", "hooks.json") }

func a36CodexHooks(f *fixture) string { return filepath.Join(f.home, ".codex", "hooks.json") }

// a36Fixture enables Cursor and Codex with foreign hooks in both user files and
// two approved canon hooks, one of them unmappable for both hosts.
func a36Fixture(t *testing.T) *fixture {
	t.Helper()

	f := newFixture(t)
	f.enableAgent(t, agent.CursorID)
	f.enableAgent(t, agent.CodexID)
	cursorHome(t, f)

	write(t, filepath.Join(f.home, ".codex", "config.toml"), "")

	write(t, a36CursorHooks(f), `{
  "version": 1,
  "hooks": {
    "preToolUse": [{"command": "foreign-tool"}]
  }
}
`)
	write(t, a36CodexHooks(f), `{
  "hooks": {
    "Stop": [{"hooks": [{"type": "command", "command": "foreign-stop"}]}]
  }
}
`)

	f.config.ApproveHook("session")
	f.config.ApproveHook("notify")

	if err := f.config.Save(f.vault.ConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	write(t, f.vault.HooksPath(), `{
  "session": {"event": "session-start", "command": "bash ~/hooks/session.sh", "timeout": 30},
  "notify": {"event": "notification", "command": "bash ~/hooks/notify.sh"}
}`)

	return f
}

// a36ReadJSON decodes one hooks file.
func a36ReadJSON(t *testing.T, path string) map[string]any {
	t.Helper()

	var doc map[string]any

	if err := json.Unmarshal([]byte(read(t, path)), &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	return doc
}

// a36Events returns the hooks object of a hooks document.
func a36Events(t *testing.T, path string) map[string]any {
	t.Helper()

	events, ok := a36ReadJSON(t, path)["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("%s has no hooks object", path)
	}

	return events
}

// a36HasText reports one report line with the substring.
func a36HasText(lines []string, substr string) bool {
	for _, line := range lines {
		if strings.Contains(line, substr) {
			return true
		}
	}

	return false
}

func TestHooksFilePresentation(t *testing.T) {
	Convey("Given Cursor and Codex enabled with approved canon hooks", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := a36Fixture(t)

		Convey("When sync runs", func() {
			report := f.sync(t)

			Convey("Then both user hooks files carry the canon hook and keep the foreign ones", func() {
				cursor := a36Events(t, a36CursorHooks(f))
				So(cursor["sessionStart"], ShouldResemble, []any{
					map[string]any{"command": "bash ~/hooks/session.sh", "timeout": float64(30)},
				})
				So(cursor["preToolUse"], ShouldResemble, []any{
					map[string]any{"command": "foreign-tool"},
				})
				So(a36ReadJSON(t, a36CursorHooks(f))["version"], ShouldEqual, 1)

				codex := a36Events(t, a36CodexHooks(f))
				So(codex["SessionStart"], ShouldResemble, []any{
					map[string]any{"hooks": []any{
						map[string]any{"type": "command", "command": "bash ~/hooks/session.sh", "timeout": float64(30)},
					}},
				})
				So(codex["Stop"], ShouldResemble, []any{
					map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "foreign-stop"}}},
				})
			})

			Convey("Then the unmappable notification hook warns for both hosts", func() {
				So(a36HasText(report.Warnings, "no cursor event"), ShouldBeTrue)
				So(a36HasText(report.Warnings, "no codex event"), ShouldBeTrue)
			})

			Convey("Then the state records the rendered commands", func() {
				st, err := state.Load(f.vault.StatePath())
				So(err, ShouldBeNil)
				So(st.HookRenders["cursor"], ShouldResemble, []string{"bash ~/hooks/session.sh"})
				So(st.HookRenders["codex"], ShouldResemble, []string{"bash ~/hooks/session.sh"})
			})

			Convey("Then a second sync is a no-op", func() {
				beforeCursor := read(t, a36CursorHooks(f))
				beforeCodex := read(t, a36CodexHooks(f))

				again := f.sync(t)
				So(again.Notes, ShouldBeEmpty)
				So(read(t, a36CursorHooks(f)), ShouldEqual, beforeCursor)
				So(read(t, a36CodexHooks(f)), ShouldEqual, beforeCodex)
			})
		})
	})
}

func TestHooksFilePresentationRemoval(t *testing.T) {
	Convey("Given a synced hooks presentation", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := a36Fixture(t)
		f.sync(t)

		Convey("When the canon hooks are revoked", func() {
			f.config.RevokeHook("session")
			f.config.RevokeHook("notify")

			if err := f.config.Save(f.vault.ConfigPath()); err != nil {
				t.Fatalf("save config: %v", err)
			}

			f.sync(t)

			Convey("Then beadle entries are gone and the foreign hooks stay", func() {
				cursor := a36Events(t, a36CursorHooks(f))
				So(cursor, ShouldNotContainKey, "sessionStart")
				So(cursor["preToolUse"], ShouldResemble, []any{map[string]any{"command": "foreign-tool"}})

				codex := a36Events(t, a36CodexHooks(f))
				So(codex, ShouldNotContainKey, "SessionStart")
				So(codex["Stop"], ShouldResemble, []any{
					map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "foreign-stop"}}},
				})
			})
		})
	})
}

func TestHooksFilePresentationSkips(t *testing.T) {
	Convey("Given approved canon hooks and seeded host files", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := a36Fixture(t)

		Convey("When a dry run syncs", func() {
			report := f.run(t, engine.SyncOptions{DryRun: true})

			Convey("Then nothing is written and the note explains the pending render", func() {
				So(a36HasText(report.Notes, "would render"), ShouldBeTrue)
				So(a36Events(t, a36CursorHooks(f)), ShouldNotContainKey, "sessionStart")
				So(a36Events(t, a36CodexHooks(f)), ShouldNotContainKey, "SessionStart")
			})
		})

		Convey("When a pull-only sync runs", func() {
			f.run(t, engine.SyncOptions{Direction: config.ModePull})

			Convey("Then the host hooks files stay untouched", func() {
				So(a36Events(t, a36CursorHooks(f)), ShouldNotContainKey, "sessionStart")
				So(a36Events(t, a36CodexHooks(f)), ShouldNotContainKey, "SessionStart")
			})
		})
	})
}

func TestHooksFileDoctorTrustInfo(t *testing.T) {
	Convey("Given Codex with rendered hooks", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := a36Fixture(t)
		stubDaemonUnit(t, f)

		f.sync(t)

		Convey("When doctor runs", func() {
			issues, err := f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then the Codex hash-trust step is an Info", func() {
				So(hasIssueFor(issues, engine.SeverityInfo, agent.CodexID, "", "/hooks"), ShouldBeTrue)
			})
		})

		Convey("When the rendered entries are removed from the file", func() {
			write(t, a36CodexHooks(f), `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "foreign-stop"}]}]}}`)

			Convey("Then the trust Info is gone", func() {
				issues, err := f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)
				So(hasIssueFor(issues, engine.SeverityInfo, agent.CodexID, "", "/hooks"), ShouldBeFalse)
			})
		})
	})
}

func TestHooksFilePresentationRemovalFromMixedGroup(t *testing.T) {
	Convey("Given a mixed Codex group whose canon hook is revoked", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := a36Fixture(t)
		f.sync(t)

		// The user merged our handler with a foreign one in the same group.
		write(t, a36CodexHooks(f), `{"hooks": {"Stop": [{"hooks": [
			{"type": "command", "command": "foreign-stop"},
			{"type": "command", "command": "bash ~/hooks/session.sh"}
		]}]}}`)

		f.config.RevokeHook("session")
		f.config.RevokeHook("notify")

		if err := f.config.Save(f.vault.ConfigPath()); err != nil {
			t.Fatalf("save config: %v", err)
		}

		report := f.sync(t)

		Convey("Then our handler is cut out, the foreign one stays and the state clears", func() {
			So(a36HasText(report.Warnings, "mixes beadle and foreign handlers"), ShouldBeTrue)
			So(a36Events(t, a36CodexHooks(f))["Stop"], ShouldResemble, []any{
				map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "foreign-stop"}}},
			})

			st, err := state.Load(f.vault.StatePath())
			So(err, ShouldBeNil)
			So(st.HookRenders["codex"], ShouldBeEmpty)
		})
	})
}

func TestHooksFileSymlinkAndMode(t *testing.T) {
	Convey("Given a symlinked Cursor hooks file with a restricted mode", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := a36Fixture(t)

		target := filepath.Join(t.TempDir(), "hooks-target.json")
		write(t, target, `{"version": 1, "hooks": {}}`)
		So(os.Chmod(target, 0o640), ShouldBeNil) //nolint:gosec // G302: loosened on purpose to check the mode is preserved
		So(os.Remove(a36CursorHooks(f)), ShouldBeNil)
		So(os.Symlink(target, a36CursorHooks(f)), ShouldBeNil)

		Convey("When sync runs", func() {
			f.sync(t)

			Convey("Then the link stays and the target carries the hook with its mode", func() {
				info, err := os.Lstat(a36CursorHooks(f))
				So(err, ShouldBeNil)
				So(info.Mode()&fs.ModeSymlink, ShouldNotEqual, fs.FileMode(0))

				So(a36Events(t, target)["sessionStart"], ShouldResemble, []any{
					map[string]any{"command": "bash ~/hooks/session.sh", "timeout": float64(30)},
				})

				targetInfo, err := os.Stat(target)
				So(err, ShouldBeNil)
				So(targetInfo.Mode().Perm(), ShouldEqual, fs.FileMode(0o640))
			})
		})
	})
}

func TestHooksFileConcurrentChange(t *testing.T) {
	Convey("Given a hooks file that changes between the plan and the write", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := a36Fixture(t)
		cursor := a36CursorHooks(f)

		restore := engine.SetHookPresentSeamForTest(func(path string) {
			if path == cursor {
				write(t, path, `{"version": 1, "hooks": {}}`)
			}
		})
		t.Cleanup(restore)

		report := f.sync(t)

		Convey("Then the write is skipped with a warning and the concurrent bytes stay", func() {
			So(a36HasText(report.Warnings, "changed concurrently; skipped"), ShouldBeTrue)
			So(a36Events(t, cursor), ShouldNotContainKey, "sessionStart")
			So(a36Events(t, a36CodexHooks(f)), ShouldContainKey, "SessionStart")
		})
	})
}

func TestHooksFilePresentationNotDetected(t *testing.T) {
	Convey("Given Cursor enabled but not detected", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.enableAgent(t, agent.CursorID)

		f.config.ApproveHook("session")

		if err := f.config.Save(f.vault.ConfigPath()); err != nil {
			t.Fatalf("save config: %v", err)
		}

		write(t, f.vault.HooksPath(), `{"session": {"event": "session-start", "command": "bash ~/hooks/session.sh"}}`)

		report := f.sync(t)

		Convey("Then no host file appears and nothing is reported", func() {
			So(fsutil.Exists(a36CursorHooks(f)), ShouldBeFalse)
			So(a36HasText(report.Notes, "hooks:"), ShouldBeFalse)
		})
	})
}

func TestHooksFilePresentationEmptyCanon(t *testing.T) {
	Convey("Given detected hosts without a hooks canon", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.enableAgent(t, agent.CursorID)
		f.enableAgent(t, agent.CodexID)
		cursorHome(t, f)

		write(t, filepath.Join(f.home, ".codex", "config.toml"), "")

		report := f.sync(t)

		Convey("Then no hooks file is created and nothing is reported", func() {
			So(fsutil.Exists(a36CursorHooks(f)), ShouldBeFalse)
			So(fsutil.Exists(a36CodexHooks(f)), ShouldBeFalse)
			So(a36HasText(report.Notes, "hooks:"), ShouldBeFalse)
		})
	})
}
