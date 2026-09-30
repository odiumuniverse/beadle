package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/verger/pkg/apply"
	"github.com/odiumuniverse/verger/pkg/verger"
)

// newLocalPackage writes a plugin package the library can install, in the shape
// the library's own tests use: a manifest at the root and a skill tree beside
// it.
func newLocalPackage(t *testing.T, home string) string {
	t.Helper()

	dir := filepath.Join(home, "pkg")

	if err := os.MkdirAll(filepath.Join(dir, "skills", "verify-skill"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	manifest := `{"schemaVersion":1,"name":"verify-pkg","version":"0.1.0","description":"d"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("manifest: %v", err)
	}

	skill := "---\nname: verify-skill\ndescription: p\n---\n\nb\n"
	if err := os.WriteFile(filepath.Join(dir, "skills", "verify-skill", "SKILL.md"), []byte(skill), 0o600); err != nil {
		t.Fatalf("skill: %v", err)
	}

	return dir
}

// runCLISplit runs a command with stdout and stderr kept apart, which is what
// the --json contract is about: a machine reads stdout, a human reads the
// progress on stderr, and neither has to strip the other.
func runCLISplit(t *testing.T, args ...string) (string, string, error) {
	t.Helper()

	root := newRootCmd(testOptions())

	var stdout, stderr bytes.Buffer

	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)
	root.SilenceUsage = true

	err := root.ExecuteContext(t.Context())

	return stdout.String(), stderr.String(), err
}

// TestPluginsInstallAppliesAndPrintsTheReport is the leg the previous
// verification could not complete: the mutating command must actually install,
// and --json must print the library's report rather than a plan's lines.
func TestPluginsInstallAppliesAndPrintsTheReport(t *testing.T) {
	Convey("Given a fresh vault and a local package", t, func() {
		home := t.TempDir()
		t.Setenv("HOME", home)
		isolateTestRoots(t)
		t.Setenv("npm_config_prefix", filepath.Join(home, "npm"))

		pkg := newLocalPackage(t, home)

		// A host is "detected" when its configuration directory exists, which
		// is what a machine that has run Claude Code once looks like.
		So(os.MkdirAll(filepath.Join(home, ".claude", "skills"), 0o700), ShouldBeNil)

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		Convey("When plugins install runs with --yes --json", func() {
			out, progress, runErr := runCLISplit(t, "plugins", "install", pkg, "--yes", "--json")
			So(runErr, ShouldBeNil)

			So(progress, ShouldNotBeEmpty) // the run says what it did, on stderr

			Convey("Then the output is the library's apply report", func() {
				var report apply.Report
				So(json.Unmarshal([]byte(out), &report), ShouldBeNil)
				So(report.Cells, ShouldNotBeEmpty)

				applied := false

				for _, cell := range report.Cells {
					if strings.Contains(cell.Package, "verify-pkg") {
						applied = true
					}
				}

				So(applied, ShouldBeTrue)
			})

			Convey("Then the package is installed and delivered to the host", func() {
				list, listErr := runCLI(t, "plugins", "list")
				So(listErr, ShouldBeNil)
				So(list, ShouldContainSubstring, "verify-pkg")

				_, statErr := os.Stat(filepath.Join(home, ".claude", "skills", "verify-skill"))
				So(statErr, ShouldBeNil)
			})

			Convey("Then plugins remove takes it back out", func() {
				removed, _, removeErr := runCLISplit(t, "plugins", "remove", "local:verify-pkg", "--json")
				So(removeErr, ShouldBeNil)

				var report apply.Report
				So(json.Unmarshal([]byte(removed), &report), ShouldBeNil)
				So(report.Cells, ShouldNotBeEmpty)

				list, listErr := runCLI(t, "plugins", "list")
				So(listErr, ShouldBeNil)
				So(list, ShouldNotContainSubstring, "verify-pkg")

				_, statErr := os.Stat(filepath.Join(home, ".claude", "skills", "verify-skill"))
				So(os.IsNotExist(statErr), ShouldBeTrue)
			})
		})
	})
}

// TestPluginsListJSONIsTheLibraryDocument pins the two output modes: --json is
// the library's own status document, and without it a human reads a table. A
// test that only checked "something was printed" would pass either way.
func TestPluginsListJSONIsTheLibraryDocument(t *testing.T) {
	Convey("Given an isolated home with a vault", t, func() {
		home := t.TempDir()
		t.Setenv("HOME", home)
		isolateTestRoots(t)

		// HOME alone is not enough, and that was the bug: the suite's TestMain pins
		// BEADLE_HOME at the shared isolated home, so this test read whichever vault
		// the previous test left there — including its spec, whose local package
		// that test's own `t.TempDir()` had already deleted. Hence
		// `source pkg: … does not exist`, naming a path belonging to another test.

		_, err := runCLI(t, "init")
		So(err, ShouldBeNil)

		Convey("When plugins list runs with --json", func() {
			out, runErr := runCLI(t, "plugins", "list", "--json")
			So(runErr, ShouldBeNil)

			Convey("Then the output parses as the library's status document", func() {
				var doc verger.StatusDocument
				So(json.Unmarshal([]byte(out), &doc), ShouldBeNil)
				So(doc.Cells, ShouldBeEmpty)
			})
		})

		Convey("When plugins list runs without --json", func() {
			out, runErr := runCLI(t, "plugins", "list")
			So(runErr, ShouldBeNil)

			Convey("Then a human reads a table in the user's words", func() {
				So(out, ShouldContainSubstring, "id")
				So(out, ShouldContainSubstring, "state")

				// §1: a strategy is not something a user chooses or acts on,
				// so the word does not appear in the human table at all.
				So(out, ShouldNotContainSubstring, "strategy")
				So(out, ShouldNotContainSubstring, "synth")
				So(out, ShouldNotContainSubstring, "hands-off")
			})
		})
	})
}
