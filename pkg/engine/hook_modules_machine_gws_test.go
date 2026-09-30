package engine_test

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/hooks"
	"github.com/odiumuniverse/beadle/pkg/state"
)

// ompMachineFixture is ompHookFixture for a machine the caller names, so a test
// can say which home the vault was on before it travelled.
func ompMachineFixture(t *testing.T, home, vaultDir string) *fixture {
	t.Helper()

	for _, name := range []string{"PI_CONFIG_DIR", "PI_CODING_AGENT_DIR", "OMP_PROFILE", "PI_PROFILE"} {
		t.Setenv(name, "")
	}

	f := newFixtureAt(t, home, vaultDir)
	f.config.Disable(agent.ClaudeCodeID)
	f.config.Disable(agent.OpenCodeID)
	f.config.Enable(agent.OmpID)

	if err := f.config.Save(f.vault.ConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	write(t, filepath.Join(ompAgentRoot(f.home), "config.yml"), "setupVersion: 2\n")

	return f
}

// arrivedMachine opens a second machine on a vault the first one wrote.
//
// The order is the whole point, and getting it wrong twice cost this test more
// time than the bug did. The vault and the host's omp tree arrive FIRST and the
// fixture is built on top of them, because a process starting on the second
// machine reads the config that arrived with it: a hook module's consent lives
// in that config, and it is keyed by plugin, phase, name and bytes - none of
// which is a path, so it survives the trip. A fixture that built its config
// first and copied the vault over it kept a config no user ever had, and the
// module then looked unconsented rather than foreign.
//
// The plugin registry is the other half: the copy names the first machine's
// install path, and a real second machine has the plugin under its own home,
// so a test that wants modules delivered there re-registers the plugin.
func arrivedMachine(t *testing.T, from string) *fixture {
	t.Helper()

	homeB := filepath.Join(t.TempDir(), "home", "b")

	if err := os.MkdirAll(homeB, 0o700); err != nil {
		t.Fatalf("home %s: %v", homeB, err)
	}

	// Only what the first machine actually had. A test about the claude surface
	// has no omp tree to copy, and asking for one would fail the arrival on a
	// directory the scenario never created.
	for _, tree := range []string{filepath.Join(from, ".beagle"), ompHomeDir(from)} {
		if _, err := os.Stat(tree); err != nil {
			continue
		}

		copyTreeWithTimes(t, tree, filepath.Join(homeB, filepath.Base(tree)))
	}

	b := ompMachineFixture(t, homeB, filepath.Join(homeB, ".beagle"))
	fakeOmpCLI(t)
	fakeProbe(t, "", false)

	return b
}

// rawModuleKeys reads the keys out of the persisted document instead of
// through state.Load. Load rewrites an absolute key into the portable form as
// it reads, so an assertion built on it is blind to what was actually written:
// it would pass against a build that stored a machine's home directory on
// every sync. The question here is what is on disk.
func rawModuleKeys(t *testing.T, f *fixture) []string {
	t.Helper()

	var doc struct {
		HookModules map[string]json.RawMessage `json:"hook_modules"`
	}

	if err := json.Unmarshal(stateJSON(t, f), &doc); err != nil {
		t.Fatalf("parse state: %v", err)
	}

	return slices.Sorted(maps.Keys(doc.HookModules))
}

// TestHookModuleKeysSurviveTheMachineThatWroteThem is the hook-module half of
// the vault that travels.
//
// A hook module is a file beadle copied into a host's module directory, and the
// state records the target path so a later sync knows the file is beadle's own:
// the digest is what separates "ours, may overwrite" from "a stranger's, leave
// alone". Keyed by an absolute path, that record is worthless on a second
// machine - the key names `/Users/a/...` while the file lives at `/home/b/...`,
// so beadle's own file looks foreign, is never overwritten and never removed,
// and the vault carries a key for a path that does not exist and warns about it
// on every sync.
func TestHookModuleKeysSurviveTheMachineThatWroteThem(t *testing.T) {
	Convey("Given a vault whose hook module was delivered on a machine whose home is /Users/a", t, func() {
		homeA := filepath.Join(t.TempDir(), "Users", "a")

		a := ompMachineFixture(t, homeA, filepath.Join(homeA, ".beagle"))
		fakeOmpCLI(t)
		fakeProbe(t, "", false)

		dir := ompPluginTree(t, a.home, "acme", "tool", "1.0.0")
		writeOMPHookModule(t, dir, "pre", "a.ts", "export default () => {}\n")
		approveOMPHookModules(t, a, "acme/tool", dir)
		a.sync(t)

		keysA := rawModuleKeys(t, a)
		So(keysA, ShouldNotBeEmpty)

		Convey("Then the key names the module, not the machine it was written on", func() {
			for _, key := range keysA {
				So(key, ShouldStartWith, state.HomePrefix)
			}
		})

		Convey("When the same vault is opened on a machine whose home is /home/b", func() {
			b := arrivedMachine(t, homeA)

			// beadle's own bytes are already on this machine: the module arrived
			// with the omp tree, exactly as they arrived on A.
			moduleOnB := filepath.Join(ompHookDir(b), "pre", "a.ts")
			So(read(t, moduleOnB), ShouldEqual, "export default () => {}\n")

			// The plugin here ships an updated module, and the user approves what
			// the plugin ships - the same act as on the first machine.
			dirB := ompPluginTree(t, b.home, "acme", "tool", "1.0.0")
			writeOMPHookModule(t, dirB, "pre", "a.ts", "export default () => { /* v2 */ }\n")
			approveOMPHookModules(t, b, "acme/tool", dirB)

			b.sync(t)

			Convey("Then the module beadle itself wrote is updated, not called foreign", func() {
				So(read(t, moduleOnB), ShouldEqual, "export default () => { /* v2 */ }\n")
			})

			Convey("And no note claims the file belongs to somebody else", func() {
				report := b.sync(t)
				So(strings.Join(report.Notes, "\n"), ShouldNotContainSubstring, "not beadle's")
			})

			Convey("And the record holds one key, the same one the first machine wrote", func() {
				So(rawModuleKeys(t, b), ShouldResemble, keysA)
			})

			Convey("And the first machine's home is nowhere in the state on disk", func() {
				So(strings.Contains(string(stateJSON(t, b)), homeA), ShouldBeFalse)
			})
		})
	})
}

// TestHookModuleForeignIsStillForeignOnEveryMachine is the other half, and it is
// the one that must not move. A portable key says nothing about ownership: a
// file a stranger wrote is still a stranger's after the vault travels, and
// making the key portable must not turn "beadle's" into "beadle wrote something
// at this name once, anywhere".
func TestHookModuleForeignIsStillForeignOnEveryMachine(t *testing.T) {
	Convey("Given a hook module slot a stranger filled in", t, func() {
		homeA := filepath.Join(t.TempDir(), "Users", "a")

		a := ompMachineFixture(t, homeA, filepath.Join(homeA, ".beagle"))
		fakeOmpCLI(t)
		fakeProbe(t, "", false)

		dir := ompPluginTree(t, a.home, "acme", "tool", "1.0.0")
		writeOMPHookModule(t, dir, "pre", "a.ts", "export default () => {}\n")
		approveOMPHookModules(t, a, "acme/tool", dir)

		b := arrivedMachine(t, homeA)

		moduleOnB := filepath.Join(ompHookDir(b), "pre", "a.ts")
		write(t, moduleOnB, "// somebody else's file\n")

		dirB := ompPluginTree(t, b.home, "acme", "tool", "1.0.0")
		writeOMPHookModule(t, dirB, "pre", "a.ts", "export default () => {}\n")
		approveOMPHookModules(t, b, "acme/tool", dirB)

		Convey("When the sync finds beadle's own name already taken", func() {
			report := b.sync(t)

			Convey("Then the file is left exactly as it was", func() {
				So(read(t, moduleOnB), ShouldEqual, "// somebody else's file\n")
			})

			Convey("And it is said to be foreign, in plain words", func() {
				So(strings.Join(report.Notes, "\n"), ShouldContainSubstring, "not beadle's")
			})

			Convey("And no record is written claiming it", func() {
				So(loadState(t, b).HookModules, ShouldBeEmpty)
			})
		})
	})
}

// TestHookModuleRecordSurvivesTheMachineThatWroteThem covers the record itself
// and the consent behind it: the digest that decides ownership has to travel,
// or a file beadle wrote is indistinguishable from a file a stranger wrote.
func TestHookModuleRecordSurvivesTheMachineThatWroteThem(t *testing.T) {
	Convey("Given a recorded module on a machine whose home is /Users/a", t, func() {
		homeA := filepath.Join(t.TempDir(), "Users", "a")

		a := ompMachineFixture(t, homeA, filepath.Join(homeA, ".beagle"))
		fakeOmpCLI(t)
		fakeProbe(t, "", false)

		dir := ompPluginTree(t, a.home, "acme", "tool", "1.0.0")
		writeOMPHookModule(t, dir, "pre", "a.ts", "export default () => {}\n")
		approveOMPHookModules(t, a, "acme/tool", dir)
		a.sync(t)

		recordA := singleModule(t, loadState(t, a))

		Convey("When the same vault is opened on a machine whose home is /home/b", func() {
			b := arrivedMachine(t, homeA)

			Convey("Then the consent travelled on its own, because it names no path", func() {
				So(b.config.HookApproved(hooks.HookModuleKey(
					recordA.Source, recordA.Phase, recordA.Name, recordA.Digest)), ShouldBeTrue)
			})

			// Same bytes as the first machine ships, so nothing is written: the
			// file on this machine is already beadle's, and a second sync has to
			// say so rather than deliver again.
			dirB := ompPluginTree(t, b.home, "acme", "tool", "1.0.0")
			writeOMPHookModule(t, dirB, "pre", "a.ts", "export default () => {}\n")

			b.sync(t)

			Convey("Then the record is the same claim: same source, phase, name and bytes", func() {
				recordB := singleModule(t, loadState(t, b))
				So(recordB.Source, ShouldEqual, recordA.Source)
				So(recordB.Phase, ShouldEqual, recordA.Phase)
				So(recordB.Name, ShouldEqual, recordA.Name)
				So(recordB.Digest, ShouldEqual, recordA.Digest)
			})

			Convey("And nothing was delivered a second time", func() {
				report := b.sync(t)
				So(strings.Join(report.Notes, "\n"), ShouldNotContainSubstring, "module(s) written into")
			})
		})
	})
}

func singleModule(t *testing.T, st *state.State) state.HookModule {
	t.Helper()

	if len(st.HookModules) != 1 {
		t.Fatalf("want exactly one recorded module, got %d: %v", len(st.HookModules), st.HookModules)
	}

	for _, record := range st.HookModules {
		return record
	}

	return state.HookModule{}
}
