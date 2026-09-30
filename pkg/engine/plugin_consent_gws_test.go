package engine_test

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/cas"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/hooks"
	"github.com/odiumuniverse/beadle/pkg/state"
	"github.com/odiumuniverse/verger/pkg/digest"
)

// The consent half of the plugin manager. The library is the only source of
// truth for a plugin's hook approval: beadle records the user's consent there,
// against the hash of the hooks the user was shown, and nowhere else.
func (m *recordingManager) ApproveHooksFor(pkgID, _ string, hash digest.Hash) (string, error) {
	if m.consents == nil {
		m.consents = map[string]digest.Hash{}
	}

	m.consents[pkgID] = hash

	return pkgID, nil
}

func (m *recordingManager) RevokeHooksFor(pkgID, _ string) error {
	m.revoked = append(m.revoked, pkgID)

	delete(m.consents, pkgID)

	return nil
}

// applyAfter is the path whose existence says the kinds have already pulled
// and rendered this run. A test points it at a file only the sync writes, so
// ApplyPackages can see for itself whether it runs last.
func (m *recordingManager) ApplyPackages(context.Context, bool) (*state.PackagesReport, error) {
	if m.applyAfter != "" {
		_, err := os.Stat(m.applyAfter)
		m.appliedAfterKinds = err == nil
	}

	m.applyCalls++

	return &state.PackagesReport{Results: []state.PackageResult{}}, nil
}

func (m *recordingManager) SyncPackages(context.Context, bool) error { return nil }

func (m *recordingManager) HooksApprovedFor(pkgID, _ string, hash digest.Hash) (bool, error) {
	approved, ok := m.consents[pkgID]

	return ok && approved == hash, nil
}

// consentMachine is a vault with one installed plugin and a manager that keeps
// consent the way the embedded library does.
func consentMachine(t *testing.T) (*fixture, *recordingManager) {
	t.Helper()

	mgr := &recordingManager{home: t.TempDir()}
	f := newFixture(t, engine.WithPluginManager(mgr))

	a31InstallPlugin(t, f, "acme", "tool", a31Hooks)

	return f, mgr
}

func TestPluginHookConsent(t *testing.T) {
	Convey("Given an installed plugin with command hooks", t, func() {
		f, mgr := consentMachine(t)

		Convey("When the user approves the plugin's hooks", func() {
			report, err := f.engine.ApprovePluginHooks("acme/tool")
			So(err, ShouldBeNil)

			Convey("Then the consent is the library's, keyed by package", func() {
				So(mgr.consents, ShouldContainKey, "plugin:acme/tool")
			})

			Convey("Then the approval is not a name in config.json", func() {
				for _, name := range f.config.ApprovedHooks {
					So(name, ShouldNotEqual, "tool--post-tool-1")
					So(name, ShouldNotEqual, "tool--session-start-1")
				}
			})

			Convey("Then the report says what was approved", func() {
				So(report.Notes, ShouldNotBeEmpty)
			})

			Convey("Then approving twice is idempotent", func() {
				_, err := f.engine.ApprovePluginHooks("acme/tool")
				So(err, ShouldBeNil)
				So(mgr.consents, ShouldContainKey, "plugin:acme/tool")
			})

			Convey("Then an edited hook no longer rides on the old consent", func() {
				canon := map[string]hooks.Hook{
					"tool--post-tool-1": {
						Event: "post-tool", Command: "changed.sh",
						Source: hooks.SourcePluginPrefix + "acme/tool",
					},
				}
				So(hooks.Save(f.vault.HooksPath(), canon), ShouldBeNil)

				approved, err := mgr.HooksApprovedFor("plugin:acme/tool", "", pluginHookSetHashForTest(canon, "acme/tool"))
				So(err, ShouldBeNil)
				So(approved, ShouldBeFalse)
			})
		})

		Convey("When the user takes the consent back", func() {
			_, err := f.engine.ApprovePluginHooks("acme/tool")
			So(err, ShouldBeNil)

			So(f.engine.RevokePluginHookConsent("acme/tool"), ShouldBeNil)
			So(mgr.revoked, ShouldResemble, []string{"plugin:acme/tool"})
			So(mgr.consents, ShouldBeEmpty)
		})
	})
}

func TestPluginHookConsentMigration(t *testing.T) {
	Convey("Given a vault whose config still names the approved plugin hooks", t, func() {
		f, mgr := consentMachine(t)

		canon := map[string]hooks.Hook{
			"tool--post-tool-1": {
				Event: "post-tool", Command: "a.sh",
				Source: hooks.SourcePluginPrefix + "acme/tool",
			},
			"authored": {Event: "stop", Command: "mine.sh"},
		}
		So(hooks.Save(f.vault.HooksPath(), canon), ShouldBeNil)

		f.config.ApproveHook("tool--post-tool-1")
		f.config.ApproveHook("authored")
		So(f.config.Save(f.vault.ConfigPath()), ShouldBeNil)

		Convey("When the vault is synchronized", func() {
			report := f.run(t, engine.SyncOptions{})

			Convey("Then the plugin hook consent moved into the library", func() {
				So(mgr.consents, ShouldContainKey, "plugin:acme/tool")
			})

			Convey("Then the name-keyed entry is gone from the config", func() {
				So(f.config.HookApproved("tool--post-tool-1"), ShouldBeFalse)
			})

			Convey("Then the approval beadle still owns is untouched", func() {
				So(f.config.HookApproved("authored"), ShouldBeTrue)
			})

			Convey("Then the move is visible, never silent", func() {
				So(joinLines(report.Notes), ShouldContainSubstring, "acme/tool")
				So(joinLines(report.Notes), ShouldContainSubstring, "consent")
			})

			Convey("Then a second run moves nothing and says nothing", func() {
				report := f.run(t, engine.SyncOptions{})
				So(joinLines(report.Notes), ShouldNotContainSubstring, "moved")
			})
		})
	})
}

func TestPluginHookConsentNeedsTheLibrary(t *testing.T) {
	Convey("Given a beadle built without the plugin library", t, func() {
		// A beadle with no library has nowhere to keep the consent, so the
		// approval must be refused instead of quietly written to config.json.
		f := newFixture(t, engine.WithPluginManager(nil))
		a31InstallPlugin(t, f, "acme", "tool", a31Hooks)

		Convey("When the user approves a plugin's hooks", func() {
			_, err := f.engine.ApprovePluginHooks("acme/tool")
			So(err, ShouldBeError)
			So(err.Error(), ShouldContainSubstring, "library")
		})
	})
}

func joinLines(lines []string) string {
	var out strings.Builder

	for _, line := range lines {
		out.WriteString(line + "\n")
	}

	return out.String()
}

// pluginHookSetHashForTest recomputes the hash beadle records, so the test can
// tell a stale consent from a live one without pinning the encoding.
func pluginHookSetHashForTest(canon map[string]hooks.Hook, pluginKey string) digest.Hash {
	type entry struct {
		Name    string `json:"name"`
		Event   string `json:"event"`
		Matcher string `json:"matcher,omitempty"`
		Command string `json:"command"`
		Timeout int    `json:"timeout,omitempty"`
	}

	entries := []entry{}

	for _, name := range slices.Sorted(maps.Keys(canon)) {
		hook := canon[name]

		key, fromPlugin := hook.PluginKey()
		if !fromPlugin || key != pluginKey {
			continue
		}

		entries = append(entries, entry{
			Name: name, Event: hook.Event, Matcher: hook.Matcher,
			Command: hook.Command, Timeout: hook.Timeout,
		})
	}

	data, err := json.Marshal(entries)
	if err != nil {
		panic(err)
	}

	return digest.Hash(cas.HashOf(data))
}

// The three properties the consent contract is made of, each pinned on its own:
// a consent covers exactly the hash it was given, a name-keyed approval is
// migrated once, and the migration is never silent.
func TestApproveHooksForProperties(t *testing.T) {
	Convey("Given a plugin whose hooks the user approved", t, func() {
		f, mgr := consentMachine(t)

		canon := map[string]hooks.Hook{
			"tool--post-tool-1": {
				Event: "post-tool", Command: "a.sh",
				Source: hooks.SourcePluginPrefix + "acme/tool",
			},
		}
		So(hooks.Save(f.vault.HooksPath(), canon), ShouldBeNil)

		_, err := f.engine.ApprovePluginHooks("acme/tool")
		So(err, ShouldBeNil)

		Convey("1. The consent is hash-keyed: another hash is not approved", func() {
			// The canon as approve left it, which is what the consent covers.
			onDisk, err := hooks.Load(f.vault.HooksPath())
			So(err, ShouldBeNil)

			live, err := mgr.HooksApprovedFor("plugin:acme/tool", "", pluginHookSetHashForTest(onDisk, "acme/tool"))
			So(err, ShouldBeNil)
			So(live, ShouldBeTrue)

			for _, hash := range []digest.Hash{"", "deadbeef", "other"} {
				approved, err := mgr.HooksApprovedFor("plugin:acme/tool", "", hash)
				So(err, ShouldBeNil)
				So(approved, ShouldBeFalse)
			}

			Convey("And an edited hook is a different hash, so it asks again", func() {
				edited := map[string]hooks.Hook{
					"tool--post-tool-1": {
						Event: "post-tool", Command: "b.sh",
						Source: hooks.SourcePluginPrefix + "acme/tool",
					},
				}

				approved, err := mgr.HooksApprovedFor("plugin:acme/tool", "", pluginHookSetHashForTest(edited, "acme/tool"))
				So(err, ShouldBeNil)
				So(approved, ShouldBeFalse)
			})
		})

		Convey("2. A name-keyed approval migrates once, and says it every time it moves one", func() {
			f2, mgr2 := consentMachine(t)

			So(hooks.Save(f2.vault.HooksPath(), canon), ShouldBeNil)
			f2.config.ApproveHook("tool--post-tool-1")
			So(f2.config.Save(f2.vault.ConfigPath()), ShouldBeNil)

			first := f2.run(t, engine.SyncOptions{})
			So(joinLines(first.Notes), ShouldContainSubstring, "moved 1 hook approval(s) of acme/tool")
			So(f2.config.HookApproved("tool--post-tool-1"), ShouldBeFalse)
			So(mgr2.consents, ShouldContainKey, "plugin:acme/tool")

			Convey("Then a second run moves nothing and stays quiet", func() {
				second := f2.run(t, engine.SyncOptions{})
				So(joinLines(second.Notes), ShouldNotContainSubstring, "moved")
				So(joinLines(second.Warnings), ShouldNotContainSubstring, "cannot move")
			})

			Convey("Then a restored backup of the old config does not re-record a live consent", func() {
				// The name comes back (a backup restores it), but the library
				// already holds that exact consent: the run must not re-record
				// it, and must not claim it moved something.
				f2.config.ApproveHook("tool--post-tool-1")
				So(f2.config.Save(f2.vault.ConfigPath()), ShouldBeNil)

				third := f2.run(t, engine.SyncOptions{})
				So(joinLines(third.Notes), ShouldNotContainSubstring, "moved")
				So(joinLines(third.Warnings), ShouldNotContainSubstring, "cannot move")
			})
		})

		Convey("3. The consent is not silent: the command shows it", func() {
			// The report is what the CLI prints, so the note is user-visible and
			// not only a field in a struct nobody reads.
			report, err := f.engine.ApprovePluginHooks("acme/tool")
			So(err, ShouldBeNil)
			So(joinLines(report.Notes), ShouldContainSubstring, "acme/tool")
			So(joinLines(report.Notes), ShouldContainSubstring, "approved")
		})
	})
}
