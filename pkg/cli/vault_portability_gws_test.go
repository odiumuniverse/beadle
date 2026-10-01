package cli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// guide/humans.md answers "Can I copy the vault to a second machine?" with yes:
// with `git clone`, or by copying the directory, and the second machine then
// delivers everything for itself.
//
// Four shapes is what that sentence covers in practice, and they are not the
// same bytes:
//
//   - `cp -a` of the vault: everything, including the machine-local state that
//     the first machine accumulated;
//   - `git clone`: the tracked structure only, because the vault's own
//     .gitignore keeps state/, state.json, objects/, conflicts/, plugins/,
//     bundles/ and projects/ on the machine that made them;
//   - a copy by a tool that skips dotfiles: no .gitignore, everything else;
//   - a checkout of the tracked files by something that skips dotfiles: the
//     canon and config.json, and none of the machine-local state.
//
// The first two are the two the guide names. The other two are what users
// actually do, and the fourth is the shape that used to be refused outright:
// `vaultDirExists` decided "this is a vault" by looking for .gitignore, state/
// or objects/ - a dotfile many copy paths drop, and two entries the vault's own
// .gitignore refuses to let leave the machine. The file that makes a directory
// a vault is config.json, and it is the one file that always travels.
//
// Every shape is asserted the same way, and the load-bearing half is the last
// one: a delivery record that came with the vault is a fact about the OTHER
// machine, so a host that has never had a file here is not a host that deleted
// one. That is where a first sync on a second machine is most likely to invent
// a deletion, and an invented deletion is a line of user output saying beadle
// removed something it never touched.

func TestTheVaultIsUsableOnASecondMachine(t *testing.T) {
	t.Cleanup(dropSharedBeadle)

	Convey("Given a machine that initialised its vault, enabled an agent and synced", t, func() {
		first := newMachine(t)
		So(first.enableAgent("claude"), ShouldBeNil)

		_, err := first.beadle("sync")
		So(err, ShouldBeNil)

		Convey("When the vault arrives four different ways", func() {
			copied := portableCopy(t, first)
			cloned := portableClone(t, first)
			noDotfiles := portableCopyWithoutDotfiles(t, first)
			trackedOnly := portableTrackedOnly(t, first)

			for _, second := range []struct {
				shape string
				m     *machine
			}{
				{"cp -a", copied},
				{"git clone", cloned},
				{"a copy without dotfiles", noDotfiles},
				{"the tracked files alone", trackedOnly},
			} {
				Convey("Then the "+second.shape+" machine delivers for itself", func() {
					So(second.m.enableAgent("claude"), ShouldBeNil)

					out, syncErr := second.m.beadle("sync")

					Convey("Then the run succeeds and the canon lands on this host", func() {
						So(syncErr, ShouldBeNil)
						So(portableHostSkill(second.m), ShouldBeTrue)
					})

					Convey("Then nothing is reported as deleted", func() {
						// Case-insensitive on purpose: the word reaches a user
						// in a sentence, not as a field name, and a check that
						// only matched one spelling would pass on the other.
						So(strings.Contains(strings.ToLower(out), "deleted"), ShouldBeFalse)
					})
				})
			}
		})
	})
}

// A vault that travels is a vault whose .gitignore did not always travel with
// it. The file is a dotfile, so a copy by a tool that skips dotfiles arrives
// without it, and so does a checkout of the tracked files. The guide promises
// credential values stay out of git, and a vault with no rules in it keeps that
// promise only until the user types `git init && git add -A` — which is exactly
// what a user does with a vault somebody sent them.
//
// The repair belongs on the accepted path and only there: `resolveVault`
// refuses a directory that is not a vault before it goes near the file, which is
// what TestCommandsDoNotHealForeignDirs pins from the other side. `status` is
// deliberately not in this list — it is a read command, and a read command that
// repairs is a read command that can fail on a read-only mount.
//
// The command below is `agents enable` and not `sync`, and that is the whole
// finding: `sync` already reached EnsureGitIgnore by way of plugin ownership,
// so it repaired the file by accident, while every other command that resolves
// a vault did not repair it at all. Enabling the agent is also the first thing
// guide/humans.md tells a user to do on a second machine, which makes it the
// command where the promise has to hold first.
func TestAVaultThatTraveledKeepsItsSecretsOutOfGit(t *testing.T) {
	t.Cleanup(dropSharedBeadle)

	// The rules the promise rests on: a secret, and the two trees that record
	// what one machine did.
	required := []string{"mcp/secrets.json", "state/", "verger/state/", "verger/store/", "objects/"}

	Convey("Given a vault that arrived without its .gitignore, holding a secret and a machine record", t, func() {
		arrived := portableArrivedWithState(t)

		portableWrite(t, filepath.Join(arrived.vault, "mcp", "secrets.json"), `{"token":"never-in-git"}`)
		portableWrite(t, filepath.Join(arrived.vault, "state", "receipts", "host.json"), "{}")

		ignorePath := filepath.Join(arrived.vault, ".gitignore")

		// The premise, asserted: without this the rest of the case would be
		// about a vault that already carries the file.
		_, statErr := os.Stat(ignorePath)
		So(os.IsNotExist(statErr), ShouldBeTrue)

		Convey("When a command that writes runs on this machine", func() {
			So(arrived.enableAgent("claude"), ShouldBeNil)

			Convey("Then the vault carries every rule the promise rests on", func() {
				ignore := portableRead(t, ignorePath)

				for _, line := range required {
					So(ignore, ShouldContainSubstring, line)
				}
			})

			Convey("Then a git add -A on this machine stages neither of them", func() {
				portableGitRepo(t, arrived.vault)

				staged := portableGitLines(t, arrived.vault, "ls-files")

				So(staged, ShouldNotContain, "mcp/secrets.json")
				So(staged, ShouldNotContain, "state/receipts/host.json")

				// And the assertion above is not the empty index: the canon
				// is staged, so git did look at this directory.
				So(staged, ShouldContain, "config.json")
			})

			Convey("Then the next command leaves the file byte for byte and does not touch it", func() {
				before, err := os.Stat(ignorePath)
				So(err, ShouldBeNil)

				bytes := portableRead(t, ignorePath)

				// A different command on purpose: the file is read, not
				// rewritten, and that has to hold across commands and not
				// just for a second run of the one that created it.
				_, againErr := arrived.beadle("sync")
				So(againErr, ShouldBeNil)

				So(portableRead(t, ignorePath), ShouldEqual, bytes)

				// The mtime is the half the bytes cannot show: a rewrite of
				// identical content leaves the bytes and takes the mtime.
				after, err := os.Stat(ignorePath)
				So(err, ShouldBeNil)
				So(after.ModTime(), ShouldEqual, before.ModTime())
			})
		})
	})

	Convey("Given a vault that arrived with a .gitignore of the user's own", t, func() {
		arrived := portableArrivedWithState(t)

		// A user's own rules, in the user's own order, with a comment and a
		// negation - none of it beadle's, all of it theirs to keep.
		own := "# my own vault\n*.tmp\n!keep.tmp\n"
		portableWrite(t, filepath.Join(arrived.vault, ".gitignore"), own)

		Convey("When a command that writes runs on this machine", func() {
			So(arrived.enableAgent("claude"), ShouldBeNil)

			_, syncErr := arrived.beadle("sync")
			So(syncErr, ShouldBeNil)

			Convey("Then their lines are untouched above ours", func() {
				ignore := portableRead(t, filepath.Join(arrived.vault, ".gitignore"))

				// Prefix, not contains: an append that reordered or rewrote
				// the user's half would still contain all of it.
				So(strings.HasPrefix(ignore, own), ShouldBeTrue)
				So(ignore, ShouldContainSubstring, "mcp/secrets.json")
				So(ignore, ShouldContainSubstring, "state/")
			})
		})
	})
}

// portableArrivedWithState is a vault on a second machine, in the shape that
// loses the most: a copy by a tool that skips dotfiles, so the .gitignore is
// gone and everything the first machine accumulated is still here.
func portableArrivedWithState(t *testing.T) *machine {
	t.Helper()

	first := newMachine(t)
	So(first.enableAgent("claude"), ShouldBeNil)

	_, err := first.beadle("sync")
	So(err, ShouldBeNil)

	return portableCopyWithoutDotfiles(t, first)
}

// portableWrite and portableRead are this file's own file helpers. The ones in
// cli_test.go and export_gws_test.go live in the internal test package and are
// not visible from here; duplicating them under a name of my own is cheaper
// than moving a file another worker owns.
func portableWrite(t *testing.T, path, body string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil { //nolint:gosec // G703: a test-local path inside the test's own vault
		t.Fatal(err)
	}
}

func portableRead(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // G304: a test-local path inside the test's own vault
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

// The other half of loosening the marker — a directory that is neither a vault
// nor a vault that lost its config — is already covered, and better, by
// TestCommandsDoNotHealForeignDirs in config_gws_test.go: a foreign directory,
// `status` reports and exits 0, `sync` refuses with the typed error, and
// nothing is created there. Adding it here would have been the same claim in a
// second place, and two copies of one claim are one copy too many.

// ---- the four shapes ---------------------------------------------------------

// portableCopy is `cp -a`: the whole directory, machine-local state included.
func portableCopy(t *testing.T, from *machine) *machine {
	t.Helper()

	home := t.TempDir()
	vault := filepath.Join(home, ".beadle")

	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}

	// `cp -a` and not a Go copy: the guide names this command, and a shape
	// assembled in Go is a shape nobody has ever used.
	if out, err := exec.CommandContext(t.Context(), "cp", "-a", from.vault, vault).CombinedOutput(); err != nil { //nolint:gosec // G204: the tool is cp, both paths are this test's own temp dirs
		t.Fatalf("cp -a: %v\n%s", err, out)
	}

	return portableMachine(t, home, vault)
}

// portableClone is `git clone`: the tracked structure only, which is the point
// of the vault's .gitignore and therefore the only shape a shared canon has.
func portableClone(t *testing.T, from *machine) *machine {
	t.Helper()

	repo := filepath.Join(t.TempDir(), "canon")
	if err := copyTree(from.vault, repo); err != nil {
		t.Fatalf("stage the repo: %v", err)
	}

	// A real repository, committed with the real tool: a hand-copied subset of
	// the tracked files is a different shape and gets its own case.
	portableGitRepo(t, repo)

	home := t.TempDir()
	vault := filepath.Join(home, ".beadle")

	if out, err := exec.CommandContext(t.Context(), "git", "clone", "--quiet", repo, vault).CombinedOutput(); err != nil { //nolint:gosec // G204: the tool is git, both paths are this test's own temp dirs
		t.Fatalf("git clone: %v\n%s", err, out)
	}

	return portableMachine(t, home, vault)
}

// portableGitRepo turns a directory into a committed repository. The identity
// is passed on the command line, so the result does not depend on whatever the
// machine running this has configured, and the commit is not signed, so a
// machine with a signing key does not have to have a passphrase.
func portableGitRepo(t *testing.T, dir string) {
	t.Helper()

	who := []string{
		"-c", "user.email=portability@example.invalid",
		"-c", "user.name=portability",
		"-c", "commit.gpgsign=false",
	}

	for _, args := range [][]string{
		{"init", "--quiet"},
		{"add", "--all"},

		append(append([]string{}, who...), "commit", "--quiet", "--message", "canon"),
	} {
		cmd := exec.CommandContext(t.Context(), "git", args...) //nolint:gosec // G204: the tool is git, the args are literals
		cmd.Dir = dir

		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=portability", "GIT_AUTHOR_EMAIL=portability@example.invalid",
			"GIT_COMMITTER_NAME=portability", "GIT_COMMITTER_EMAIL=portability@example.invalid")

		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}

// portableCopyWithoutDotfiles is a copy by a tool that skips dotfiles: no
// .gitignore, everything else, machine-local state included.
func portableCopyWithoutDotfiles(t *testing.T, from *machine) *machine {
	t.Helper()

	home := t.TempDir()
	vault := filepath.Join(home, ".beadle")

	if err := portableCopyTreeSkippingDotfiles(from.vault, vault); err != nil {
		t.Fatalf("copy without dotfiles: %v", err)
	}

	return portableMachine(t, home, vault)
}

// portableTrackedOnly is the shape that used to be refused: the canon and
// config.json, and nothing else. It is what a checkout by a tool that skips
// dotfiles produces, and it is the smallest thing the guide promises travels.
func portableTrackedOnly(t *testing.T, from *machine) *machine {
	t.Helper()

	repo := filepath.Join(t.TempDir(), "canon")
	if err := copyTree(from.vault, repo); err != nil {
		t.Fatalf("stage the repo: %v", err)
	}

	portableGitRepo(t, repo)

	tracked := portableGitLines(t, repo, "ls-files")

	home := t.TempDir()
	vault := filepath.Join(home, ".beadle")

	for _, name := range tracked {
		// The .gitignore is the dotfile this shape leaves behind, and leaving
		// it behind is the whole difference between this case and the clone.
		if name == ".gitignore" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(repo, name)) //nolint:gosec // G304: a path under the test's own staged repo
		if err != nil {
			t.Fatalf("read tracked %s: %v", name, err)
		}

		target := filepath.Join(vault, name)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(target, data, 0o600); err != nil { //nolint:gosec // G703: target is inside the test's own vault, built from git's own listing
			t.Fatal(err)
		}
	}

	return portableMachine(t, home, vault)
}

// portableGitLines runs one read-only git query in dir and returns its lines.
func portableGitLines(t *testing.T, dir string, args ...string) []string {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "git", args...) //nolint:gosec // G204: the tool is git, the args are literals

	cmd.Dir = dir

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}

	var lines []string

	for line := range strings.SplitSeq(string(out), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}

	return lines
}

// portableCopyTreeSkippingDotfiles copies a tree and leaves every name that
// starts with a dot behind, at every level: the behaviour of the copy tools
// that skip them, which is a real way a vault arrives.
func portableCopyTreeSkippingDotfiles(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		for part := range strings.SplitSeq(rel, string(filepath.Separator)) {
			if strings.HasPrefix(part, ".") && part != "." {
				if info.IsDir() {
					return filepath.SkipDir
				}

				return nil
			}
		}

		target := filepath.Join(dst, rel)

		if info.IsDir() {
			return os.MkdirAll(target, 0o700)
		}

		data, err := os.ReadFile(path) //nolint:gosec // G304: a path Walk produced under the test's own tree
		if err != nil {
			return err
		}

		return os.WriteFile(target, data, 0o600) //nolint:gosec // G703: a test-local destination
	})
}

// portableMachine is the second machine: a different home, a vault that arrived
// some other way, and nothing from the first home reachable.
func portableMachine(t *testing.T, home, vault string) *machine {
	t.Helper()

	t.Setenv("HOME", home)
	t.Setenv("BEADLE_HOME", vault)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("DSH_HOME", filepath.Join(home, ".dsh"))

	return &machine{t: t, home: home, vault: vault}
}

// portableHostSkill is the canon where it lands on a host: the shared skills
// root, which is where the enabled agent reads it from.
//
// A function and not a method on `machine`, because that type belongs to
// another worker's test file: a second file adding methods to it is a name
// collision waiting to happen, and the collision would break their build and
// mine at once.
func portableHostSkill(m *machine) bool {
	_, err := os.Stat(filepath.Join(m.home, ".agents", "skills", "beadle", "SKILL.md"))

	return err == nil
}
