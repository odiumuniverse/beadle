package engine_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
)

const (
	registryRewritePause = 2 * time.Millisecond
	pivotReadAttempts    = 5
)

type ledgerTestRecord struct {
	Version       string    `json:"version"`
	Target        string    `json:"target"`
	Servers       []string  `json:"servers,omitempty"`
	QuarantinedAt time.Time `json:"quarantined_at"`
	RetiredAt     time.Time `json:"retired_at"`
}

func quarantineDir(f *fixture, marketplace, name string) string {
	return filepath.Join(f.vault.PluginsDir(), "quarantine", marketplace, name)
}

func claudePluginsDir(home string) string {
	return filepath.Join(home, ".claude", "plugins")
}

func registryPath(home string) string {
	return filepath.Join(claudePluginsDir(home), "installed_plugins.json")
}

func pluginTree(t *testing.T, home, marketplace, name, version string) string {
	t.Helper()

	dir := filepath.Join(claudePluginsDir(home), "cache", marketplace, name, version)
	write(t, filepath.Join(dir, ".claude-plugin", "plugin.json"), fmt.Sprintf(`{"name": %q, "version": %q}`, name, version))

	plugins := readRegistry(t, home)
	plugins[name+"@"+marketplace] = []map[string]any{{
		"scope":       "user",
		"installPath": dir,
		"version":     version,
	}}

	writeRegistry(t, home, plugins)

	return dir
}

func readRegistry(t *testing.T, home string) map[string][]map[string]any {
	t.Helper()

	data, err := os.ReadFile(registryPath(home))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string][]map[string]any{}
	}

	if err != nil {
		t.Fatalf("read registry: %v", err)
	}

	var doc struct {
		Plugins map[string][]map[string]any `json:"plugins"`
	}

	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal registry: %v", err)
	}

	if doc.Plugins == nil {
		doc.Plugins = map[string][]map[string]any{}
	}

	return doc.Plugins
}

func registryBytes(t *testing.T, plugins map[string][]map[string]any) []byte {
	t.Helper()

	data, err := json.MarshalIndent(map[string]any{"plugins": plugins}, "", "  ")
	if err != nil {
		t.Fatalf("marshal registry: %v", err)
	}

	return append(data, '\n')
}

func writeRegistry(t *testing.T, home string, plugins map[string][]map[string]any) {
	t.Helper()

	if err := os.MkdirAll(claudePluginsDir(home), 0o750); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}

	if err := os.WriteFile(registryPath(home), registryBytes(t, plugins), 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}
}

func removeFromRegistry(t *testing.T, home, marketplace, name string) {
	t.Helper()

	plugins := readRegistry(t, home)
	delete(plugins, name+"@"+marketplace)

	writeRegistry(t, home, plugins)
}

func pluginResult(t *testing.T, report *engine.Report, key string) engine.PluginResult {
	t.Helper()

	for _, result := range report.Plugins {
		if result.Key == key {
			return result
		}
	}

	t.Fatalf("no plugin result for %s", key)

	return engine.PluginResult{}
}

func pivotLink(t *testing.T, f *fixture, marketplace, name string) string {
	t.Helper()

	link, err := readPivotLink(filepath.Join(f.vault.PluginsDir(), marketplace, name, "current"), pivotReadAttempts)
	if err != nil {
		t.Fatalf("read pivot link: %v", err)
	}

	return link
}

func readPivotLink(path string, attempts int) (string, error) {
	var err error

	for range attempts {
		var link string

		link, err = os.Readlink(path)
		if err == nil {
			return link, nil
		}
	}

	return "", err
}

func ledgerRecord(t *testing.T, f *fixture, key string) ledgerTestRecord {
	t.Helper()

	var doc struct {
		Version int                         `json:"version"`
		Plugins map[string]ledgerTestRecord `json:"plugins"`
	}

	if err := json.Unmarshal([]byte(read(t, f.vault.PluginsLedgerPath())), &doc); err != nil {
		t.Fatalf("unmarshal ledger: %v", err)
	}

	if doc.Version != 1 {
		t.Fatalf("ledger version %d", doc.Version)
	}

	rec, ok := doc.Plugins[key]
	if !ok {
		t.Fatalf("ledger must contain %s: %v", key, doc.Plugins)
	}

	return rec
}

func startWorker(t *testing.T, worker func(stop <-chan struct{})) func() {
	t.Helper()

	stop := make(chan struct{})

	var (
		once sync.Once
		wg   sync.WaitGroup
	)

	stopWorker := func() {
		once.Do(func() { close(stop) })

		wg.Wait()
	}

	t.Cleanup(stopWorker)

	wg.Go(func() {
		worker(stop)
	})

	return stopWorker
}

func countPath(paths []string, path string) int {
	count := 0

	for _, candidate := range paths {
		if candidate == path {
			count++
		}
	}

	return count
}

func warnedWith(warnings []string, substr string) bool {
	return slices.ContainsFunc(warnings, func(warning string) bool {
		return strings.Contains(warning, substr)
	})
}

func TestPluginReconcileCreatesAndRepointsPivot(t *testing.T) {
	Convey("Given a plugin cache entry", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		target := pluginTree(t, f.home, "acme", "tool", "1.0.0")

		report := f.sync(t)
		result := pluginResult(t, report, "acme/tool")

		info, err := os.Stat(f.vault.PluginsLedgerPath())
		So(err, ShouldBeNil)

		Convey("When it is created", func() {
			So(result.Action, ShouldEqual, engine.PluginCreated)
			So(result.Version, ShouldEqual, "1.0.0")
			So(result.Target, ShouldEqual, target)
			So(pivotLink(t, f, "acme", "tool"), ShouldEqual, target)
			So(info.Mode().Perm(), ShouldEqual, os.FileMode(0o600))
			So(ledgerRecord(t, f, "acme/tool").Version, ShouldEqual, "1.0.0")
		})

		Convey("When it upgrades and then stabilizes", func() {
			upgraded := pluginTree(t, f.home, "acme", "tool", "2.0.0")

			report = f.sync(t)
			result = pluginResult(t, report, "acme/tool")

			So(result.Action, ShouldEqual, engine.PluginRepointed)
			So(result.Version, ShouldEqual, "2.0.0")
			So(pivotLink(t, f, "acme", "tool"), ShouldEqual, upgraded)

			_, oldErr := os.Stat(target)
			So(oldErr, ShouldBeNil)
			So(ledgerRecord(t, f, "acme/tool").Version, ShouldEqual, "2.0.0")

			before := read(t, f.vault.PluginsLedgerPath())

			report = f.sync(t)
			result = pluginResult(t, report, "acme/tool")

			Convey("Then noop leaves the ledger alone", func() {
				So(result.Action, ShouldEqual, engine.PluginNoop)
				So(read(t, f.vault.PluginsLedgerPath()), ShouldEqual, before)
				So(pivotLink(t, f, "acme", "tool"), ShouldEqual, upgraded)
			})
		})
	})
}

func TestPluginReconcileReadersSeeOldOrNew(t *testing.T) {
	Convey("Given a concurrent pivot reader during an upgrade", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		v1 := pluginTree(t, f.home, "acme", "tool", "1.0.0")

		f.sync(t)

		pivot := filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current")

		var (
			mu       sync.Mutex
			seen     []string
			failures []string
		)

		fail := func(message string) {
			mu.Lock()

			failures = append(failures, message)

			mu.Unlock()
		}

		record := func(link string) {
			mu.Lock()

			seen = append(seen, link)

			mu.Unlock()
		}

		stopReader := startWorker(t, func(stop <-chan struct{}) {
			for {
				select {
				case <-stop:
					return
				default:
				}

				link, err := readPivotLink(pivot, pivotReadAttempts)
				if err != nil {
					fail("readlink: " + err.Error())

					return
				}

				if !fsutil.Exists(link) {
					fail("target does not exist: " + link)

					return
				}

				record(link)
			}
		})

		v2 := pluginTree(t, f.home, "acme", "tool", "2.0.0")

		f.sync(t)
		stopReader()

		report := f.sync(t)

		Convey("When the pivot is repointed", func() {
			Convey("Then readers only ever see a live target", func() {
				So(failures, ShouldBeEmpty)
				So(seen, ShouldNotBeEmpty)

				for _, link := range seen {
					So(link, ShouldBeIn, []string{v1, v2})
				}

				So(pivotLink(t, f, "acme", "tool"), ShouldEqual, v2)

				_, v1Err := os.Stat(v1)
				So(v1Err, ShouldBeNil)

				So(pluginResult(t, report, "acme/tool").Action, ShouldEqual, engine.PluginNoop)
			})
		})
	})
}

func TestPluginReconcileRegistryRace(t *testing.T) {
	Convey("Given a registry rewritten concurrently", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		v1 := pluginTree(t, f.home, "acme", "tool", "1.0.0")

		v2 := filepath.Join(claudePluginsDir(f.home), "cache", "acme", "tool", "2.0.0")
		write(t, filepath.Join(v2, ".claude-plugin", "plugin.json"), `{"name": "tool", "version": "2.0.0"}`)

		regV1 := registryBytes(t, map[string][]map[string]any{"tool@acme": {{
			"scope": "user", "installPath": v1, "version": "1.0.0",
		}}})

		regV2 := registryBytes(t, map[string][]map[string]any{"tool@acme": {{
			"scope": "user", "installPath": v2, "version": "2.0.0",
		}}})

		var (
			mu       sync.Mutex
			failures []string
		)

		fail := func(message string) {
			mu.Lock()

			failures = append(failures, message)

			mu.Unlock()
		}

		stopWriter := startWorker(t, func(stop <-chan struct{}) {
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}

				blob := regV1
				if i%2 == 1 {
					blob = regV2
				}

				if err := fsutil.WriteFileAtomic(registryPath(f.home), blob, 0o600); err != nil {
					fail("rewrite registry: " + err.Error())

					return
				}

				time.Sleep(registryRewritePause)
			}
		})

		for range 5 {
			f.sync(t)
		}

		stopWriter()

		f.sync(t)

		link := pivotLink(t, f, "acme", "tool")
		rec := ledgerRecord(t, f, "acme/tool")

		Convey("When the race ends", func() {
			Convey("Then the ledger matches a live pivot", func() {
				So(failures, ShouldBeEmpty)
				So(fsutil.Exists(link), ShouldBeTrue)
				So(rec.Version, ShouldBeIn, []string{"1.0.0", "2.0.0"})
				So(link, ShouldEqual, rec.Target)
			})
		})
	})
}

func TestPluginReconcileSkipsMissingInstallPath(t *testing.T) {
	Convey("Given a registry entry whose install path is missing", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		missing := filepath.Join(claudePluginsDir(f.home), "cache", "acme", "ghost", "1.0.0")
		writeRegistry(t, f.home, map[string][]map[string]any{"ghost@acme": {{
			"scope": "user", "installPath": missing, "version": "1.0.0",
		}}})

		report := f.sync(t)
		result := pluginResult(t, report, "acme/ghost")

		Convey("When sync runs", func() {
			_, ledgerErr := os.Stat(f.vault.PluginsLedgerPath())

			Convey("Then it is skipped and no ledger is written", func() {
				So(result.Action, ShouldEqual, engine.PluginSkipped)
				So(result.Note, ShouldEqual, "install path is missing")
				So(errors.Is(ledgerErr, fs.ErrNotExist), ShouldBeTrue)
			})
		})
	})
}

func TestPluginReconcileDryRunMakesNoChanges(t *testing.T) {
	Convey("Given a dry run", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		pluginTree(t, f.home, "acme", "tool", "1.0.0")

		report := f.run(t, engine.SyncOptions{DryRun: true})

		_, ledgerErr := os.Stat(f.vault.PluginsLedgerPath())
		_, acmeErr := os.Stat(filepath.Join(f.vault.PluginsDir(), "acme"))
		_, pluginsErr := os.Stat(f.vault.PluginsDir())

		Convey("When it previews", func() {
			Convey("Then nothing is registered", func() {
				So(report.Plugins, ShouldBeEmpty)
				So(pluginsErr, ShouldBeNil)
				So(errors.Is(ledgerErr, fs.ErrNotExist), ShouldBeTrue)
				So(errors.Is(acmeErr, fs.ErrNotExist), ShouldBeTrue)
			})
		})
	})
}

func TestPluginReconcileQuarantinesMissingTarget(t *testing.T) {
	Convey("Given a parked plugin whose target disappears", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		target := pluginTree(t, f.home, "acme", "tool", "1.0.0")

		f.sync(t)
		So(os.RemoveAll(target), ShouldBeNil)

		report := f.sync(t)
		result := pluginResult(t, report, "acme/tool")

		pivot := filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current")

		So(result.Action, ShouldEqual, engine.PluginQuarantined)
		So(result.Version, ShouldEqual, "1.0.0")

		_, pivotErr := os.Stat(pivot)
		So(errors.Is(pivotErr, fs.ErrNotExist), ShouldBeTrue)

		link, err := os.Readlink(filepath.Join(quarantineDir(f, "acme", "tool"), "current"))
		So(err, ShouldBeNil)
		So(link, ShouldEqual, target)

		rec := ledgerRecord(t, f, "acme/tool")
		So(rec.QuarantinedAt.IsZero(), ShouldBeFalse)
		So(rec.Version, ShouldEqual, "1.0.0")
		So(rec.Target, ShouldEqual, target)

		before := read(t, f.vault.PluginsLedgerPath())

		report = f.sync(t)
		result = pluginResult(t, report, "acme/tool")

		Convey("When a repeat sync runs", func() {
			Convey("Then it is skipped and the ledger is untouched", func() {
				So(result.Action, ShouldEqual, engine.PluginSkipped)
				So(result.Note, ShouldEqual, "quarantined already")
				So(read(t, f.vault.PluginsLedgerPath()), ShouldEqual, before)
			})
		})
	})
}

func TestPluginReconcileQuarantinesRemovedRegistry(t *testing.T) {
	Convey("Given a removed registry entry and target", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		target := pluginTree(t, f.home, "acme", "tool", "1.0.0")

		f.sync(t)

		removeFromRegistry(t, f.home, "acme", "tool")
		So(os.RemoveAll(target), ShouldBeNil)

		report := f.sync(t)
		result := pluginResult(t, report, "acme/tool")

		link, err := os.Readlink(filepath.Join(quarantineDir(f, "acme", "tool"), "current"))
		So(err, ShouldBeNil)

		Convey("When it quarantines", func() {
			Convey("Then the quarantine link points at the removed target", func() {
				So(result.Action, ShouldEqual, engine.PluginQuarantined)
				So(result.Note, ShouldNotEqual, "plugin is no longer installed")
				So(ledgerRecord(t, f, "acme/tool").QuarantinedAt.IsZero(), ShouldBeFalse)
				So(link, ShouldEqual, target)
			})
		})
	})
}

func TestPluginReconcileKeepsLivePivot(t *testing.T) {
	Convey("Given a registry pointing at a missing version while a pivot lives", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		v1 := pluginTree(t, f.home, "acme", "tool", "1.0.0")

		f.sync(t)

		missing := filepath.Join(claudePluginsDir(f.home), "cache", "acme", "tool", "2.0.0")

		writeRegistry(t, f.home, map[string][]map[string]any{"tool@acme": {{
			"scope": "user", "installPath": missing, "version": "2.0.0",
		}}})

		report := f.sync(t)
		result := pluginResult(t, report, "acme/tool")

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When sync runs", func() {
			_, quarantineErr := os.Stat(quarantineDir(f, "acme", "tool"))

			Convey("Then the last good pivot is kept and doctor errors", func() {
				So(result.Action, ShouldEqual, engine.PluginSkipped)
				So(result.Note, ShouldEqual, "install path is missing")
				So(pivotLink(t, f, "acme", "tool"), ShouldEqual, v1)
				So(errors.Is(quarantineErr, fs.ErrNotExist), ShouldBeTrue)
				So(ledgerRecord(t, f, "acme/tool").QuarantinedAt.IsZero(), ShouldBeTrue)

				So(hasIssue(issues, engine.SeverityError, "plugin acme/tool install path is missing"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityError, "pivot target is missing"), ShouldBeFalse)
			})
		})
	})
}

func TestPluginReconcileNoQuarantineWithoutRecord(t *testing.T) {
	Convey("Given a foreign pivot with no ledger record", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		missing := filepath.Join(claudePluginsDir(f.home), "cache", "acme", "tool", "1.0.0")
		pivot := filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current")

		So(os.MkdirAll(filepath.Dir(pivot), 0o700), ShouldBeNil)
		So(os.Symlink(missing, pivot), ShouldBeNil)

		writeRegistry(t, f.home, map[string][]map[string]any{"tool@acme": {{
			"scope": "user", "installPath": missing, "version": "1.0.0",
		}}})

		report := f.sync(t)
		result := pluginResult(t, report, "acme/tool")

		_, quarantineErr := os.Stat(quarantineDir(f, "acme", "tool"))
		_, ledgerErr := os.Stat(f.vault.PluginsLedgerPath())

		Convey("When sync runs", func() {
			Convey("Then nothing is quarantined without ownership", func() {
				So(result.Action, ShouldEqual, engine.PluginSkipped)
				So(result.Note, ShouldEqual, "install path is missing")
				So(pivotLink(t, f, "acme", "tool"), ShouldEqual, missing)
				So(errors.Is(quarantineErr, fs.ErrNotExist), ShouldBeTrue)
				So(errors.Is(ledgerErr, fs.ErrNotExist), ShouldBeTrue)
			})
		})
	})
}

func TestPluginReconcileReinstallClearsQuarantine(t *testing.T) {
	Convey("Given a quarantined plugin that is reinstalled", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		target := pluginTree(t, f.home, "acme", "tool", "1.0.0")

		f.sync(t)
		So(os.RemoveAll(target), ShouldBeNil)

		report := f.sync(t)
		So(pluginResult(t, report, "acme/tool").Action, ShouldEqual, engine.PluginQuarantined)

		write(t, filepath.Join(target, ".claude-plugin", "plugin.json"), `{"name": "tool", "version": "1.0.0"}`)

		pivot := filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current")
		So(fsutil.ReplaceSymlink(pivot, target), ShouldBeNil)

		report = f.sync(t)

		result := pluginResult(t, report, "acme/tool")

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When it is restored", func() {
			Convey("Then the quarantine flag clears and doctor is quiet", func() {
				So(result.Action, ShouldEqual, engine.PluginRepointed)
				So(pivotLink(t, f, "acme", "tool"), ShouldEqual, target)
				So(ledgerRecord(t, f, "acme/tool").QuarantinedAt.IsZero(), ShouldBeTrue)

				for _, issue := range issues {
					So(issue.Message, ShouldNotContainSubstring, "acme/tool")
				}
			})
		})
	})
}

func TestDoctorPluginPivotIssues(t *testing.T) {
	Convey("Given a sequence of plugin pivot states", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		v1 := pluginTree(t, f.home, "acme", "tool", "1.0.0")

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When each state is diagnosed", func() {
			So(hasIssue(issues, engine.SeverityWarn, "plugin acme/tool is not parked yet"), ShouldBeTrue)

			f.sync(t)

			v2 := pluginTree(t, f.home, "acme", "tool", "2.0.0")

			issues, err = f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)
			So(hasIssue(issues, engine.SeverityWarn, "plugin acme/tool changed 1.0.0 → 2.0.0"), ShouldBeTrue)

			f.sync(t)

			So(os.RemoveAll(v2), ShouldBeNil)

			issues, err = f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)
			So(hasIssue(issues, engine.SeverityError, "plugin acme/tool pivot target is missing"), ShouldBeTrue)

			So(os.MkdirAll(v2, 0o700), ShouldBeNil)
			f.sync(t)

			So(fsutil.ReplaceSymlink(filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current"), v1), ShouldBeNil)

			issues, err = f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)
			So(hasIssue(issues, engine.SeverityWarn, "plugin acme/tool pivot is stale: "+v1), ShouldBeTrue)

			pivotPath := filepath.Join(f.vault.PluginsDir(), "acme", "tool", "current")
			So(os.Remove(pivotPath), ShouldBeNil)
			write(t, pivotPath, "not a symlink")

			issues, err = f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)
			So(hasIssue(issues, engine.SeverityWarn, "plugin acme/tool pivot cannot be read"), ShouldBeTrue)

			removeFromRegistry(t, f.home, "acme", "tool")
			So(os.RemoveAll(v1), ShouldBeNil)
			So(os.RemoveAll(v2), ShouldBeNil)

			issues, err = f.engine.Doctor(t.Context())
			So(err, ShouldBeNil)

			Convey("Then doctor reports each state and never a pivot error for a removed plugin", func() {
				So(hasIssue(issues, engine.SeverityWarn, "plugin acme/tool is no longer installed"), ShouldBeTrue)
				So(hasIssue(issues, engine.SeverityError, "acme/tool"), ShouldBeFalse)
			})
		})
	})
}

func TestWatchPathsIncludePluginRegistry(t *testing.T) {
	Convey("Given a marketplace checkout and registry", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		head := filepath.Join(claudePluginsDir(f.home), "marketplaces", "acme", ".git", "HEAD")
		write(t, head, "ref: refs/heads/main\n")

		Convey("When watch paths are listed", func() {
			paths, err := f.engine.WatchPaths(t.Context())
			So(err, ShouldBeNil)

			registry := registryPath(f.home)

			Convey("Then the registry and HEAD are each watched once", func() {
				So(countPath(paths, registry), ShouldEqual, 1)
				So(countPath(paths, head), ShouldEqual, 1)
			})
		})
	})
}

func TestPluginReconcileMalformedLedger(t *testing.T) {
	Convey("Given a malformed ledger", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		target := pluginTree(t, f.home, "acme", "tool", "1.0.0")

		So(os.MkdirAll(f.vault.PluginsDir(), 0o700), ShouldBeNil)
		So(os.WriteFile(f.vault.PluginsLedgerPath(), []byte("{ not json"), 0o600), ShouldBeNil)

		report := f.sync(t)

		var doc struct {
			Version int                         `json:"version"`
			Plugins map[string]ledgerTestRecord `json:"plugins"`
		}

		Convey("When sync runs", func() {
			So(pluginResult(t, report, "acme/tool").Action, ShouldEqual, engine.PluginCreated)
			So(pivotLink(t, f, "acme", "tool"), ShouldEqual, target)
			So(warnedWith(report.Warnings, "ledger"), ShouldBeTrue)

			So(json.Unmarshal([]byte(read(t, f.vault.PluginsLedgerPath())), &doc), ShouldBeNil)

			Convey("Then the ledger is rebuilt", func() {
				So(doc.Version, ShouldEqual, 1)
				So(doc.Plugins, ShouldContainKey, "acme/tool")
			})
		})
	})
}

func TestPluginReconcileKeepsLedgerForMissingPlugin(t *testing.T) {
	Convey("Given a parked plugin whose registry entry is removed", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		target := pluginTree(t, f.home, "acme", "tool", "1.0.0")

		f.sync(t)
		removeFromRegistry(t, f.home, "acme", "tool")

		report := f.sync(t)
		result := pluginResult(t, report, "acme/tool")

		Convey("When sync runs", func() {
			Convey("Then the ledger and pivot are kept", func() {
				So(result.Action, ShouldEqual, engine.PluginSkipped)
				So(result.Note, ShouldEqual, "plugin is no longer installed")
				So(ledgerRecord(t, f, "acme/tool").Version, ShouldEqual, "1.0.0")
				So(pivotLink(t, f, "acme", "tool"), ShouldEqual, target)
			})
		})
	})
}

func TestPluginReconcileRejectsInvalidKeys(t *testing.T) {
	Convey("Given a registry entry with a traversal key", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		pluginTree(t, f.home, "acme", "tool", "1.0.0")

		evil := filepath.Join(claudePluginsDir(f.home), "cache", "acme", "evil", "0.0.1")
		write(t, filepath.Join(evil, ".claude-plugin", "plugin.json"), `{"name": "evil", "version": "0.0.1"}`)

		plugins := readRegistry(t, f.home)
		plugins["../evil@acme"] = []map[string]any{{
			"scope": "user", "installPath": evil, "version": "0.0.1",
		}}

		writeRegistry(t, f.home, plugins)

		report := f.sync(t)

		bad := pluginResult(t, report, "acme/../evil")

		_, evilDirErr := os.Stat(filepath.Join(f.vault.PluginsDir(), "evil"))

		Convey("When sync runs", func() {
			Convey("Then the invalid key is rejected first and never parked", func() {
				So(bad.Action, ShouldEqual, engine.PluginSkipped)
				So(bad.Note, ShouldEqual, "invalid plugin key")
				So(warnedWith(report.Warnings, "invalid plugin key"), ShouldBeTrue)
				So(pluginResult(t, report, "acme/tool").Action, ShouldEqual, engine.PluginCreated)
				So(errors.Is(evilDirErr, fs.ErrNotExist), ShouldBeTrue)
				So(read(t, f.vault.PluginsLedgerPath()), ShouldNotContainSubstring, "evil")
				So(report.Plugins[0].Key, ShouldEqual, "acme/../evil")
			})
		})
	})
}

func TestPluginReconcileStopsWhenGitIgnoreFails(t *testing.T) {
	Convey("Given an unwritable vault .gitignore", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		pluginTree(t, f.home, "acme", "tool", "1.0.0")

		ignorePath := filepath.Join(f.vault.Root(), ".gitignore")
		So(os.Chmod(ignorePath, 0o000), ShouldBeNil)

		t.Cleanup(func() {
			_ = os.Chmod(ignorePath, 0o600)
		})

		report := f.sync(t)

		_, ledgerErr := os.Stat(f.vault.PluginsLedgerPath())
		_, acmeErr := os.Stat(filepath.Join(f.vault.PluginsDir(), "acme"))

		Convey("When sync runs", func() {
			Convey("Then it stops before parking and warns", func() {
				So(report.Plugins, ShouldBeEmpty)
				So(warnedWith(report.Warnings, ".gitignore"), ShouldBeTrue)
				So(errors.Is(ledgerErr, fs.ErrNotExist), ShouldBeTrue)
				So(errors.Is(acmeErr, fs.ErrNotExist), ShouldBeTrue)
			})
		})
	})
}

func TestEnsureGitIgnoreMerges(t *testing.T) {
	Convey("Given a vault in git history mode with an existing gitignore", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git is not installed")
		}

		t.Setenv("GIT_AUTHOR_NAME", "beadle test")
		t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
		t.Setenv("GIT_COMMITTER_NAME", "beadle test")
		t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")

		f := newFixture(t)
		f.config.History = config.HistoryGit
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		write(t, f.claudeConfig(), `{"mcpServers": {}}`)
		write(t, f.claudeRules(), "# r\n")
		write(t, f.openCodeConfig(), `{"mcp": {}}`)
		write(t, f.openCodeRules(), "# r\n")

		old := "# Beadle: credentials and machine-local state stay on this machine.\nmcp/secrets.json\nstate/\nstate.json\nobjects/\nconflicts/"
		ignorePath := filepath.Join(f.vault.Root(), ".gitignore")
		write(t, ignorePath, old)

		pluginTree(t, f.home, "acme", "tool", "1.0.0")

		f.sync(t)

		merged := read(t, ignorePath)

		info, err := os.Stat(ignorePath)
		So(err, ShouldBeNil)

		status, err := exec.CommandContext(t.Context(), "git", "-C", f.vault.Root(), "status", "--porcelain").Output() //nolint:gosec // G204: fixed git subcommand in a test
		So(err, ShouldBeNil)

		tracked, err := exec.CommandContext(t.Context(), "git", "-C", f.vault.Root(), "ls-files").Output() //nolint:gosec // G204: fixed git subcommand in a test
		So(err, ShouldBeNil)

		Convey("When a plugin is parked", func() {
			f.sync(t)
			mergedAgain := read(t, ignorePath)

			So(f.vault.EnsureGitIgnore(), ShouldBeNil)

			Convey("Then plugins are ignored and never committed, idempotently", func() {
				So(strings.HasPrefix(merged, old), ShouldBeTrue)
				So(merged, ShouldContainSubstring, "plugins/\n")
				So(strings.HasSuffix(merged, "\n"), ShouldBeTrue)
				So(info.Mode().Perm(), ShouldEqual, os.FileMode(0o600))
				So(string(status), ShouldNotContainSubstring, "plugins")
				So(string(tracked), ShouldNotContainSubstring, "plugins")
				So(mergedAgain, ShouldEqual, merged)
				So(read(t, ignorePath), ShouldEqual, merged)
			})
		})
	})
}

func TestPluginReconcileEmptyHome(t *testing.T) {
	Convey("Given an engine whose home is not set", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)
		pluginTree(t, f.home, "acme", "tool", "1.0.0")

		e, err := engine.New(f.vault, f.config, agent.All(f.home, t.TempDir()))
		So(err, ShouldBeNil)

		report, err := e.Sync(t.Context(), engine.SyncOptions{})
		So(err, ShouldBeNil)

		issues, err := e.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When sync and doctor run", func() {
			_, ledgerErr := os.Stat(f.vault.PluginsLedgerPath())
			_, acmeErr := os.Stat(filepath.Join(f.vault.PluginsDir(), "acme"))

			Convey("Then nothing is parked and doctor is quiet", func() {
				So(report.Errors(), ShouldBeEmpty)
				So(report.Plugins, ShouldBeEmpty)
				So(errors.Is(ledgerErr, fs.ErrNotExist), ShouldBeTrue)
				So(errors.Is(acmeErr, fs.ErrNotExist), ShouldBeTrue)

				for _, issue := range issues {
					So(issue.Message, ShouldNotContainSubstring, "acme/tool")
				}
			})
		})
	})
}

func TestDoctorPluginPivotIssuesNoRegistry(t *testing.T) {
	Convey("Given a home without any plugin registry", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		issues, err := f.engine.Doctor(t.Context())
		So(err, ShouldBeNil)

		Convey("When doctor runs", func() {
			Convey("Then no pivot or plugin issue appears", func() {
				for _, issue := range issues {
					So(issue.Message, ShouldNotContainSubstring, "pivot")
					So(issue.Message, ShouldNotContainSubstring, "plugin")
				}
			})
		})
	})
}
