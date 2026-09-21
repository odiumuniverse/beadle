package engine_test

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

func writeOrphanPivot(t *testing.T, f *fixture, marketplace, name string) string {
	t.Helper()

	target := filepath.Join(t.TempDir(), "cache")

	if err := os.MkdirAll(filepath.Join(target, "skills", "caveman"), 0o750); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}

	if err := os.WriteFile(filepath.Join(target, "skills", "caveman", "SKILL.md"), []byte("# caveman\n"), 0o600); err != nil {
		t.Fatalf("write target skill: %v", err)
	}

	dir := filepath.Join(f.vault.PluginsDir(), marketplace, name)

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir pivot: %v", err)
	}

	if err := os.Symlink(target, filepath.Join(dir, "current")); err != nil {
		t.Fatalf("symlink pivot: %v", err)
	}

	return dir
}

func writeFarmLink(t *testing.T, f *fixture, dir, skill, marketplace, name string) string {
	t.Helper()

	link := filepath.Join(dir, skill)

	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir skills dir: %v", err)
	}

	target := filepath.Join(f.vault.PluginsDir(), marketplace, name, "current", "skills", skill)

	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink farm link: %v", err)
	}

	return link
}

func lookupPluginResult(report *engine.Report, key string) (engine.PluginResult, bool) {
	for _, result := range report.Plugins {
		if result.Key == key {
			return result, true
		}
	}

	return engine.PluginResult{}, false
}

func TestReconcileOrphanPivot(t *testing.T) {
	Convey("Given an orphan bundle pivot and a farm link pointing into it", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		dir := writeOrphanPivot(t, f, "beadle", "beadle-canon")
		link := writeFarmLink(t, f, filepath.Join(f.home, ".claude", "skills"), "caveman", "beadle", "beadle-canon")

		f.config.SetMode(agent.ClaudeCodeID, kind.Skills, config.ModeOff)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		report := f.sync(t)

		Convey("When sync runs with the skill mode off", func() {
			result, ok := lookupPluginResult(report, "beadle/beadle-canon")

			Convey("Then the pivot is retired, the link pruned and a repeat run is a no-op", func() {
				So(ok, ShouldBeTrue)
				So(result.Action, ShouldEqual, engine.PluginRetired)

				_, err := os.Lstat(dir)
				So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)

				_, err = os.Lstat(link)
				So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)

				pruned := false

				for _, farm := range report.Farm {
					if farm.Action == engine.FarmPruned && farm.Plugin == "beadle/beadle-canon" && farm.Agent == agent.ClaudeCodeID {
						pruned = true
					}
				}

				So(pruned, ShouldBeTrue)

				second := f.sync(t)
				_, ok = lookupPluginResult(second, "beadle/beadle-canon")
				So(ok, ShouldBeFalse)
			})
		})
	})
}

func TestReconcileOrphanSkipsBrokenLedger(t *testing.T) {
	Convey("Given a broken plugin ledger and orphan pivots", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		dir := writeOrphanPivot(t, f, "beadle", "beadle-canon")
		link := writeFarmLink(t, f, filepath.Join(f.home, ".claude", "skills"), "caveman", "beadle", "beadle-canon")

		write(t, f.vault.PluginsLedgerPath(), "{not json")

		f.sync(t)

		Convey("When sync runs", func() {
			Convey("Then nothing is retired or pruned and the ledger rebuilds without the orphan", func() {
				_, err := os.Lstat(dir)
				So(err, ShouldBeNil)

				_, err = os.Lstat(link)
				So(err, ShouldBeNil)

				var doc struct {
					Version int            `json:"version"`
					Plugins map[string]any `json:"plugins"`
				}

				So(json.Unmarshal([]byte(read(t, f.vault.PluginsLedgerPath())), &doc), ShouldBeNil)
				So(doc.Plugins, ShouldNotContainKey, "beadle/beadle-canon")
			})
		})
	})
}

func TestReconcileOrphanKeepsLivePlugin(t *testing.T) {
	Convey("Given a parked live plugin", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		pluginTree(t, f.home, "acme", "tool", "1.0.0")

		f.sync(t)

		pivot := filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current")

		Convey("When sync runs again", func() {
			report := f.sync(t)

			Convey("Then the live pivot survives", func() {
				result, ok := lookupPluginResult(report, "acme/tool")
				So(ok, ShouldBeTrue)
				So(result.Action, ShouldNotEqual, engine.PluginRetired)

				_, err := os.Lstat(pivot)
				So(err, ShouldBeNil)
			})
		})
	})
}

func TestReconcileOrphanKeepsPin(t *testing.T) {
	Convey("Given a pinned plugin that is not in the ledger", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		So(f.config.SetPluginPin(agent.ClaudeCodeID, "acme/tool", "1.0.0"), ShouldBeNil)
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		target := filepath.Join(f.home, ".claude", "plugins", "cache", "acme", "tool", "1.0.0")
		So(os.MkdirAll(target, 0o750), ShouldBeNil)

		f.sync(t)

		pivot := filepath.Join(f.vault.PluginsDir(), "acme", "tool", "at-1.0.0")

		Convey("When sync runs", func() {
			Convey("Then the pin pivot is not treated as an orphan", func() {
				_, err := os.Lstat(pivot)
				So(err, ShouldBeNil)
			})
		})
	})
}

func TestHealRetiresOrphans(t *testing.T) {
	Convey("Given orphan pivots and links", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		dir := writeOrphanPivot(t, f, "beadle", "beadle-canon")
		link := writeFarmLink(t, f, filepath.Join(f.home, ".claude", "skills"), "caveman", "beadle", "beadle-canon")

		results, err := f.engine.Heal(t.Context(), false)
		So(err, ShouldBeNil)

		Convey("When heal runs", func() {
			Convey("Then the orphans retire and a repeat run has nothing to heal", func() {
				retired := false
				pruned := false

				for _, result := range results {
					if result.Key == "beadle/beadle-canon" && result.Retired == 1 {
						retired = true
					}

					if result.Key == "beadle/beadle-canon" && result.Pruned == 1 {
						pruned = true
					}
				}

				So(retired, ShouldBeTrue)
				So(pruned, ShouldBeTrue)

				_, err := os.Lstat(dir)
				So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)

				_, err = os.Lstat(link)
				So(errors.Is(err, fs.ErrNotExist), ShouldBeTrue)

				second, err := f.engine.Heal(t.Context(), false)
				So(err, ShouldBeNil)
				So(second, ShouldBeEmpty)
			})
		})
	})
}

func TestDoctorOrphanWarnings(t *testing.T) {
	Convey("Given orphan pivots and links on disk", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		f.emptyConfigs(t)

		writeOrphanPivot(t, f, "beadle", "beadle-canon")
		writeFarmLink(t, f, filepath.Join(f.home, ".claude", "skills"), "caveman", "beadle", "beadle-canon")

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When doctor runs", func() {
			Convey("Then it warns before cleanup and stays quiet after heal", func() {
				So(hasIssue(issues, engine.SeverityWarn, "orphan pivot"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityWarn, "orphan plugin skill link"), ShouldBeTrue)

				_, err := f.engine.Heal(t.Context(), false)
				So(err, ShouldBeNil)

				issues, err = f.engine.Doctor(t.Context())
				So(err, ShouldBeNil)
				So(hasIssue(issues, engine.SeverityWarn, "orphan pivot"), ShouldBeFalse)
			})
		})
	})
}
