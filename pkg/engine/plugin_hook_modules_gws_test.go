package engine_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/cas"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/hooks"
	"github.com/odiumuniverse/beadle/pkg/hostcli"
	"github.com/odiumuniverse/beadle/pkg/plugin"
	"github.com/odiumuniverse/beadle/pkg/state"
)

// ompHookFixture enables omp and gives it a config, so the agent is active
// without a real binary on PATH.
func ompHookFixture(t *testing.T) *fixture {
	t.Helper()

	for _, name := range []string{"PI_CONFIG_DIR", "PI_CODING_AGENT_DIR", "OMP_PROFILE", "PI_PROFILE"} {
		t.Setenv(name, "")
	}

	f := ompFixture(t)
	write(t, filepath.Join(ompAgentRoot(f.home), "config.yml"), "setupVersion: 2\n")

	return f
}

// ompHookDir is the module directory of the fixture's active omp agent.
func ompHookDir(f *fixture) string {
	return filepath.Join(ompAgentRoot(f.home), "hooks")
}

func writeOMPHookModule(t *testing.T, pluginDir, phase, name, content string) {
	t.Helper()

	write(t, filepath.Join(pluginDir, "hooks", phase, name), content)
}

// approveOMPHookModules records the module consent of one plugin at its
// current bytes, exactly as `beadle hooks approve --plugin` would.
func approveOMPHookModules(t *testing.T, f *fixture, key, pluginDir string) {
	t.Helper()

	modules, _ := plugin.HookModules(plugin.SourceOMP, pluginDir)

	for _, module := range modules {
		data, err := os.ReadFile(module.Path) //nolint:gosec // G304: the test reads its own temp tree
		if err != nil {
			t.Fatalf("read module %s: %v", module.Path, err)
		}

		f.config.ApproveHook(hooks.HookModuleKey(key, module.Phase, module.Name, cas.HashOf(data)))
	}

	So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)
}

// fakeOmpCLI makes the omp CLI resolvable without a real binary.
func fakeOmpCLI(t *testing.T) {
	t.Helper()

	restore := engine.SetBundlesLookPathForTest(func(name string) (string, error) {
		if name == "omp" {
			return "/bin/echo", nil
		}

		return "", fmt.Errorf("%s: not found", name)
	})
	t.Cleanup(restore)
}

// fakeProbe replaces the headless probe and records every invocation.
func fakeProbe(t *testing.T, output string, notFound bool) *[]string {
	t.Helper()

	calls := &[]string{}

	restore := engine.SetHookModuleProbeForTest(func(_ context.Context, bin hostcli.Binary, args []string) ([]byte, bool) {
		*calls = append(*calls, bin.Name+" "+strings.Join(args, " "))

		return []byte(output), notFound
	})
	t.Cleanup(restore)

	return calls
}

func hookModuleRecord(t *testing.T, f *fixture, target string) (state.HookModule, bool) {
	t.Helper()

	st, err := state.Load(f.vault.StatePath())
	if err != nil {
		t.Fatalf("load state: %v", err)
	}

	record, ok := st.HookModules[target]

	return record, ok
}

func TestHookModulesDeliverAndRecord(t *testing.T) {
	Convey("Given a consented omp plugin shipping two hook modules", t, func() {
		f := ompHookFixture(t)
		fakeOmpCLI(t)
		fakeProbe(t, "", false)

		dir := ompPluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeOMPHookModule(t, dir, "pre", "a.ts", "export default () => {}\n")
		writeOMPHookModule(t, dir, "post", "b.js", "module.exports = () => {}\n")
		approveOMPHookModules(t, f, "acme/tool", dir)

		Convey("When the sync delivers them", func() {
			report := f.sync(t)

			Convey("Then both land byte-for-byte and are recorded", func() {
				pre := filepath.Join(ompHookDir(f), "pre", "a.ts")
				post := filepath.Join(ompHookDir(f), "post", "b.js")

				So(read(t, pre), ShouldEqual, "export default () => {}\n")
				So(read(t, post), ShouldEqual, "module.exports = () => {}\n")

				rec, ok := hookModuleRecord(t, f, pre)
				So(ok, ShouldBeTrue)
				So(rec.Source, ShouldEqual, "acme/tool")
				So(string(rec.Digest), ShouldEqual, string(cas.HashOf([]byte("export default () => {}\n"))))

				Convey("And the note carries the sandbox, audit and restart warnings", func() {
					note := strings.Join(report.Notes, "\n")
					So(note, ShouldContainSubstring, "neither sandboxes hook modules")
					So(note, ShouldContainSubstring, "did not audit")
					So(note, ShouldContainSubstring, "restart omp")
				})
			})
		})
	})
}

func TestHookModulesAwaitConsent(t *testing.T) {
	Convey("Given an omp plugin whose modules are not approved", t, func() {
		f := ompHookFixture(t)
		fakeOmpCLI(t)
		calls := fakeProbe(t, "", false)

		dir := ompPluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeOMPHookModule(t, dir, "pre", "a.ts", "export default () => {}\n")

		Convey("When the sync runs", func() {
			report := f.sync(t)

			Convey("Then nothing is written, no probe runs, and the consent is named", func() {
				So(pathExists(filepath.Join(ompHookDir(f), "pre", "a.ts")), ShouldBeFalse)
				So(*calls, ShouldBeEmpty)

				note := strings.Join(report.Notes, "\n")
				So(note, ShouldContainSubstring, "awaiting consent")
				So(note, ShouldContainSubstring, "beadle hooks approve --plugin acme/tool")
				So(note, ShouldContainSubstring, "neither sandboxes hook modules")
			})
		})
	})
}

func TestHookModulesIdempotentAndForeignSafe(t *testing.T) {
	Convey("Given a delivered module and a foreign file beside it", t, func() {
		f := ompHookFixture(t)
		fakeOmpCLI(t)
		fakeProbe(t, "", false)

		dir := ompPluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeOMPHookModule(t, dir, "pre", "a.ts", "export default () => {}\n")
		approveOMPHookModules(t, f, "acme/tool", dir)

		f.sync(t)

		foreign := filepath.Join(ompHookDir(f), "pre", "foreign.ts")
		write(t, foreign, "foreign bytes\n")

		before := read(t, filepath.Join(ompHookDir(f), "pre", "a.ts"))

		Convey("When the sync runs again with no change", func() {
			report := f.sync(t)

			Convey("Then nothing is rewritten and the foreign file is untouched", func() {
				So(read(t, filepath.Join(ompHookDir(f), "pre", "a.ts")), ShouldEqual, before)
				So(read(t, foreign), ShouldEqual, "foreign bytes\n")
				So(strings.Join(report.Notes, "\n"), ShouldNotContainSubstring, "module(s) written")
			})
		})

		Convey("When a foreign file takes our target's place", func() {
			write(t, filepath.Join(ompHookDir(f), "pre", "a.ts"), "replaced by the user\n")

			report := f.sync(t)

			Convey("Then beadle leaves it and drops its record", func() {
				So(read(t, filepath.Join(ompHookDir(f), "pre", "a.ts")), ShouldEqual, "replaced by the user\n")
				So(strings.Join(report.Notes, "\n"), ShouldContainSubstring, "left untouched")

				_, ok := hookModuleRecord(t, f, filepath.Join(ompHookDir(f), "pre", "a.ts"))
				So(ok, ShouldBeFalse)
			})
		})
	})
}

func TestHookModulesWithdrawOnlyOurs(t *testing.T) {
	Convey("Given a delivered module and a foreign file in the same directory", t, func() {
		f := ompHookFixture(t)
		fakeOmpCLI(t)
		fakeProbe(t, "", false)

		dir := ompPluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeOMPHookModule(t, dir, "pre", "a.ts", "export default () => {}\n")
		approveOMPHookModules(t, f, "acme/tool", dir)

		f.sync(t)

		foreign := filepath.Join(ompHookDir(f), "pre", "foreign.ts")
		write(t, foreign, "foreign bytes\n")

		Convey("When the consent is revoked", func() {
			f.config.RevokeHook(hooks.HookModuleKey("acme/tool", "pre", "a.ts",
				cas.HashOf([]byte("export default () => {}\n"))))
			So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

			report := f.sync(t)

			Convey("Then only the recorded module is removed", func() {
				So(pathExists(filepath.Join(ompHookDir(f), "pre", "a.ts")), ShouldBeFalse)
				So(read(t, foreign), ShouldEqual, "foreign bytes\n")
				So(strings.Join(report.Notes, "\n"), ShouldContainSubstring, "removed 1 module(s)")

				_, ok := hookModuleRecord(t, f, filepath.Join(ompHookDir(f), "pre", "a.ts"))
				So(ok, ShouldBeFalse)
			})
		})

		Convey("When the source plugin is retired", func() {
			So(os.RemoveAll(dir), ShouldBeNil)
			write(t, filepath.Join(ompHomeDir(f.home), "plugins", "installed_plugins.json"),
				`{"version":2,"plugins":{}}`)

			report := f.sync(t)

			Convey("Then the module is withdrawn in the same pass", func() {
				So(pathExists(filepath.Join(ompHookDir(f), "pre", "a.ts")), ShouldBeFalse)
				So(strings.Join(report.Notes, "\n"), ShouldContainSubstring, "removed 1 module(s)")
			})
		})
	})
}

func TestHookModulesProbeFailure(t *testing.T) {
	Convey("Given a module the load probe reports as broken", t, func() {
		f := ompHookFixture(t)
		fakeOmpCLI(t)

		line := "Failed to load extension ~/.omp/agent/hooks/pre/broken.ts: Failed to parse extension source"
		calls := fakeProbe(t, line+"\n", false)

		dir := ompPluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeOMPHookModule(t, dir, "pre", "broken.ts", "export default (((\n")
		approveOMPHookModules(t, f, "acme/tool", dir)

		Convey("When the sync delivers it", func() {
			report := f.sync(t)

			Convey("Then the delivery fails loudly and the broken file is gone", func() {
				So(*calls, ShouldHaveLength, 1)

				warnings := strings.Join(report.Warnings, "\n")
				So(warnings, ShouldContainSubstring, "delivery failed")
				So(warnings, ShouldContainSubstring, "Failed to load extension")

				So(pathExists(filepath.Join(ompHookDir(f), "pre", "broken.ts")), ShouldBeFalse)

				_, ok := hookModuleRecord(t, f, filepath.Join(ompHookDir(f), "pre", "broken.ts"))
				So(ok, ShouldBeFalse)
			})
		})
	})
}

func TestHookModulesUnverifiableWithoutCLI(t *testing.T) {
	Convey("Given an active omp with no CLI on PATH", t, func() {
		f := ompHookFixture(t)
		calls := fakeProbe(t, "", true)

		dir := ompPluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeOMPHookModule(t, dir, "pre", "a.ts", "export default () => {}\n")
		approveOMPHookModules(t, f, "acme/tool", dir)

		restore := engine.SetBundlesLookPathForTest(func(name string) (string, error) {
			return "", fmt.Errorf("%s: not found", name)
		})
		t.Cleanup(restore)

		Convey("When the sync delivers", func() {
			report := f.sync(t)

			Convey("Then the module lands and the probe is reported unverifiable", func() {
				So(read(t, filepath.Join(ompHookDir(f), "pre", "a.ts")), ShouldEqual, "export default () => {}\n")
				So(*calls, ShouldBeEmpty)
				So(strings.Join(report.Notes, "\n"), ShouldContainSubstring, "unverifiable")
			})
		})
	})
}

func TestApprovePluginHooksApprovesModules(t *testing.T) {
	Convey("Given an omp plugin with a hook module", t, func() {
		f := ompHookFixture(t)
		fakeOmpCLI(t)

		dir := ompPluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeOMPHookModule(t, dir, "pre", "a.ts", "export default () => {}\n")

		f.sync(t) // park the pivot so approve can resolve it

		before := hooks.HookModuleKey("acme/tool", "pre", "a.ts", cas.HashOf([]byte("export default () => {}\n")))

		Convey("When the plugin is approved", func() {
			_, err := f.engine.ApprovePluginHooks("acme/tool")
			So(err, ShouldBeNil)
			So(f.config.HookApproved(before), ShouldBeTrue)

			Convey("When the module bytes change and approve runs again", func() {
				writeOMPHookModule(t, dir, "pre", "a.ts", "export default () => {}\n// changed\n")

				_, err := f.engine.ApprovePluginHooks("acme/tool")
				So(err, ShouldBeNil)

				after := hooks.HookModuleKey("acme/tool", "pre", "a.ts",
					cas.HashOf([]byte("export default () => {}\n// changed\n")))

				So(f.config.HookApproved(before), ShouldBeFalse)
				So(f.config.HookApproved(after), ShouldBeTrue)
			})
		})
	})
}

func TestHookModulesSkippedWithoutOmp(t *testing.T) {
	Convey("Given an enabled omp that is not installed", t, func() {
		for _, name := range []string{"PI_CONFIG_DIR", "PI_CODING_AGENT_DIR", "OMP_PROFILE", "PI_PROFILE"} {
			t.Setenv(name, "")
		}

		f := ompFixture(t)
		calls := fakeProbe(t, "", false)

		t.Setenv("PATH", "/usr/bin:/bin")

		dir := ompPluginTree(t, f.home, "acme", "tool", "1.0.0")
		writeOMPHookModule(t, dir, "pre", "a.ts", "export default () => {}\n")
		approveOMPHookModules(t, f, "acme/tool", dir)

		Convey("When the sync runs", func() {
			f.sync(t)

			Convey("Then nothing is written and no probe runs", func() {
				So(pathExists(ompHookDir(f)), ShouldBeFalse)
				So(*calls, ShouldBeEmpty)
			})
		})
	})
}

// TestHookModulesLiveProbe is the living proof on the real omp: a module
// written in an isolated HOME writes a marker file from module scope, so the
// marker's existence proves omp imported the delivered bytes.
func TestHookModulesLiveProbe(t *testing.T) {
	if _, err := exec.LookPath("omp"); err != nil {
		t.Skip("omp is not installed; the live module probe is skipped")
	}

	Convey("Given the real omp and an isolated HOME", t, func() {
		f := ompHookFixture(t)
		t.Setenv("HOME", f.home)

		marker := filepath.Join(f.home, "hook-marker")

		dir := ompPluginTree(t, f.home, "acme", "tool", "1.0.0")

		module := "import { writeFileSync } from \"node:fs\"\n" +
			"writeFileSync(" + strconv.Quote(marker) + ", \"loaded\")\n" +
			"export default () => {}\n"

		writeOMPHookModule(t, dir, "pre", "probe.ts", module)
		approveOMPHookModules(t, f, "acme/tool", dir)

		Convey("When the sync delivers and probes it", func() {
			report := f.sync(t)

			Convey("Then omp imports the module and writes the marker", func() {
				So(read(t, filepath.Join(ompHookDir(f), "pre", "probe.ts")), ShouldEqual, module)
				So(strings.Join(report.Warnings, "\n"), ShouldNotContainSubstring, "delivery failed")
				So(pathExists(marker), ShouldBeTrue)
				So(read(t, marker), ShouldEqual, "loaded")
			})
		})
	})
}

func pathExists(path string) bool {
	_, err := os.Stat(path)

	return err == nil
}
