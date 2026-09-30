package cli

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/vergerx"
	"github.com/odiumuniverse/verger/pkg/apply"
	"github.com/odiumuniverse/verger/pkg/verger"
)

// The scenario two tools have to agree on, end to end: verger installs a
// package into a host, the user edits what verger wrote, and beadle syncs.
//
// beadle is not the writer here, so it has no business touching that file. It
// does not own the bytes — verger's receipt does — and it does not own the
// canon — verger's package does. The user's edit is not a conflict either:
// beadle never delivered the file, so it has no base to call it changed
// against. The correct run is a quiet success that leaves the file exactly as
// the user left it.
//
// This is the real scenario. The reduced one ran `beadle sync` in a home that
// had never seen `beadle init`, so it measured the uninitialized-vault path
// (class 2, "run beadle init") and filed that under the conflict gate. Anyone
// running both tools has a vault, and this is what their sync does.
func TestVergerOwnedFileSurvivesBeadleSync(t *testing.T) {
	Convey("Given a vault, a verger-installed skill and a user edit on it", t, func() {
		home := gwsHome(t)

		// A host is "detected" when its configuration directory exists, which
		// is what a machine that has run Claude Code once looks like.
		So(os.MkdirAll(filepath.Join(home, ".claude", "skills"), 0o700), ShouldBeNil)

		skill := "---\nname: gate\ndescription: gate probe\n---\n\nb\n"
		pkg := filepath.Join(home, "pkg")
		gwsWrite(t, filepath.Join(pkg, ".claude-plugin", "plugin.json"),
			`{"schemaVersion":1,"name":"gate-pkg","version":"0.1.0","description":"d"}`+"\n")
		gwsWrite(t, filepath.Join(pkg, "skills", "gate", "SKILL.md"), skill)

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		// verger installs, through the same library the binary uses and
		// against the same vault-hosted home `beadle init` just created. The
		// receipt it writes is the proof of ownership beadle has to respect.
		client, err := vergerx.Open(t.Context(), vergerx.Config{
			VaultRoot: filepath.Join(home, ".beadle"),
			Confirmer: vergerx.YesConfirmer(),
		})
		So(err, ShouldBeNil)

		plan, err := client.PlanFor(t.Context(), []string{pkg}, false,
			verger.HostFilter{Only: []string{"claude"}})
		So(err, ShouldBeNil)

		_, err = client.Install(t.Context(), plan)
		So(err, ShouldBeNil)
		So(client.Close(), ShouldBeNil)

		delivered := filepath.Join(home, ".claude", "skills", "gate", "SKILL.md")
		So(readFile(t, delivered), ShouldEqual, skill)

		receipt := filepath.Join(home, ".beadle", "verger", "state", "receipts", "local:gate-pkg")
		So(readFile(t, filepath.Join(receipt, "claude-user.json")),
			ShouldContainSubstring, "local:gate-pkg")

		// The user edits what verger wrote.
		edited := skill + "edited by the user\n"
		gwsWrite(t, delivered, edited)

		Convey("When beadle syncs", func() {
			_, syncErr := runCLI(t, "sync")

			Convey("Then the run succeeds — a verger file the user edited is not a conflict", func() {
				So(syncErr, ShouldBeNil)
			})

			Convey("And the user's file is byte-for-byte what they left", func() {
				So(readFile(t, delivered), ShouldEqual, edited)
			})

			Convey("And the skill never enters beadle's canon", func() {
				// The vault holds the receipt, which is verger's state that
				// beadle keeps; it must hold no canon copy of the skill.
				vault := snapshotTree(t, filepath.Join(home, ".beadle"))
				So(len(vault), ShouldBeGreaterThan, 0)
				So(slices.Contains(slices.Collect(maps.Keys(vault)),
					filepath.Join("verger", "state", "receipts", "local:gate-pkg", "claude-user.json")),
					ShouldBeTrue)

				for path := range vault {
					So(path, ShouldNotContainSubstring,
						filepath.Join("skills", "gate", "SKILL.md"))
				}
			})

			Convey("And verger still sees the file as the user's, not as drift to fix", func() {
				// The last link in the chain, and the one that proves the
				// two tools agree: verger owns the file, so verger is the one
				// that must refuse to overwrite it. If beadle had rewritten
				// it during the sync above, verger would now report the cell
				// as delivered and this would say so.
				//
				// It is `verger sync` that carries the word, not
				// `verger status`: status answers "is the receipt current",
				// which is a different question from "is the file still
				// what we wrote".
				client, openErr := vergerx.Open(t.Context(), vergerx.Config{
					VaultRoot: filepath.Join(home, ".beadle"),
					Confirmer: vergerx.YesConfirmer(),
				})
				So(openErr, ShouldBeNil)

				defer func() { _ = client.Close() }()

				plan, planErr := client.PlanFor(t.Context(), []string{pkg}, false,
					verger.HostFilter{Only: []string{"claude"}})
				So(planErr, ShouldBeNil)

				// force=false is the caller's `--force` withheld, which is
				// the whole point: verger is asked to write, and declines.
				report, applyErr := client.InstallFor(t.Context(), plan, false, false)
				So(applyErr, ShouldBeNil)

				Convey("Then verger leaves the cell alone rather than writing it", func() {
					var statuses []apply.Status

					for _, cell := range report.Cells {
						if cell.Host == "claude" {
							statuses = append(statuses, cell.Status)
						}
					}

					So(statuses, ShouldContain, apply.StatusHandsOff)
				})

				Convey("And the file is still the user's", func() {
					So(readFile(t, delivered), ShouldEqual, edited)
				})
			})

			Convey("And no conflict is opened over it", func() {
				conflicts, listErr := runCLI(t, "conflicts", "--json")
				So(listErr, ShouldBeNil)
				So(conflicts, ShouldContainSubstring, `"conflicts": []`)
			})
		})
	})
}

// The ownership predicate and the plugin manager are wired from one client, and
// the client can fail to open. The failure used to be discarded, so the run
// carried on with no predicate at all — which is not "no plugin files", it is
// the opposite: with nothing to consult, every host file looks unowned and the
// pull adopts it. A verger-owned artifact then enters beadle's canon and the
// two tools start fighting over the same file. Losing the ability to ask must
// stop the run, not change its answer.
func TestSyncFailsLoudlyWhenPluginOwnershipIsUnknown(t *testing.T) {
	Convey("Given a vault whose verger home cannot be opened", t, func() {
		home := gwsHome(t)
		So(os.MkdirAll(filepath.Join(home, ".claude"), 0o700), ShouldBeNil)

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		// Something else already owns the name the client has to create. It
		// fails the same way a corrupt store would.
		vergerHome := filepath.Join(home, ".beadle", "verger")
		So(os.RemoveAll(vergerHome), ShouldBeNil)
		gwsWrite(t, vergerHome, "not a directory\n")

		Convey("When beadle syncs", func() {
			_, syncErr := runCLI(t, "sync")

			Convey("Then the run stops instead of guessing who owns what", func() {
				So(syncErr, ShouldNotBeNil)
				So(syncErr.Error(), ShouldContainSubstring, "verger")
			})
		})
	})
}
