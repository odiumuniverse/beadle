package homescan_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/homescan"
)

// A watched surface that does not exist yet is the ordinary state on a
// developer machine and on CI, so most of these start from an empty home and
// create what they need.
func write(t *testing.T, path, body string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("make %s: %v", filepath.Dir(path), err)
	}

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// The contract the guard rests on: it watches what beadle writes, and it does
// not watch the rest of an agent's home. The second half is not a nicety — a
// guard over logs/ and backups/ fails for anyone running the suite with a live
// agent on the same machine, and is then switched off.
func TestRootsAreTheSurfacesAndNotTheHomesAroundThem(t *testing.T) {
	Convey("Given a home", t, func() {
		roots := homescan.Roots("/home/u")

		Convey("Then the tools' own homes are watched whole", func() {
			So(roots, ShouldContain, "/home/u/.beadle")
			So(roots, ShouldContain, "/home/u/.verger")
		})

		Convey("And inside a host's home, the surfaces beadle writes", func() {
			for _, want := range []string{
				"/home/u/.claude/skills",
				"/home/u/.claude/agents",
				"/home/u/.claude/commands",
				"/home/u/.claude/plugins",
				"/home/u/.claude/settings.json",
				"/home/u/.claude/CLAUDE.md",
				"/home/u/.claude.json",
				"/home/u/.codex/config.toml",
				"/home/u/.gemini/GEMINI.md",
				"/home/u/.config/opencode/opencode.json",
				"/home/u/.config/kilo/kilo.json",
				"/home/u/.agents/skills",
				"/home/u/.omp/plugins",
			} {
				So(roots, ShouldContain, want)
			}
		})

		Convey("And nothing a host writes for its own runtime", func() {
			// Each of these is what a live agent on this machine touches while
			// the suite runs. A guard that reported them would be reporting the
			// neighbour, not the leak.
			for _, unwanted := range []string{
				"/home/u/.claude",
				"/home/u/.claude/logs",
				"/home/u/.claude/backups",
				"/home/u/.claude/sessions",
				"/home/u/.claude/projects",
				"/home/u/.config",
				"/home/u/.omp",
				"/home/u/.omp/agent",
			} {
				So(roots, ShouldNotContain, unwanted)
			}
		})

		Convey("And no JSON pointer mistaken for a path", func() {
			// MCPPointer and MCPTable hold fragments like /mcpServers. They are
			// not paths, and a containment check written as rel != ".." lets
			// them out.
			for _, root := range roots {
				So(strings.HasPrefix(root, "/home/u/"), ShouldBeTrue)
			}
		})
	})
}

// The two halves of the narrowing, in one place: what the host does to its own
// runtime is not the guard's business, and what beadle writes is.
func TestTheBoundaryBetweenWhatTheHostWritesAndWhatBeadleWrites(t *testing.T) {
	Convey("Given a home with a claude root", t, func() {
		home := t.TempDir()
		write(t, filepath.Join(home, ".claude", "skills", "existing.md"), "x")

		before := homescan.Take(home)

		Convey("When something lands in ~/.claude/backups", func() {
			// A live claude session writes there constantly. Reporting it makes
			// the guard cry wolf on the one machine where a leak is likeliest.
			write(t, filepath.Join(home, ".claude", "backups", "2026.json"), "{}")

			Convey("Then the guard stays quiet", func() {
				So(before.Changed(before.Recheck()), ShouldBeEmpty)
			})
		})

		Convey("When something lands in ~/.claude/skills", func() {
			// The same write, one directory over, and this one is beadle's.
			write(t, filepath.Join(home, ".claude", "skills", "leaked.md"), "x")

			Convey("Then the guard names the file", func() {
				So(before.Changed(before.Recheck()), ShouldResemble,
					[]string{"created  " + filepath.Join(home, ".claude", "skills", "leaked.md")})
			})
		})
	})
}

// The guard's root is a parameter so that a test which must write where the
// guard watches has somewhere harmless to write. These pin the default — the
// machine's own home — because a guard that quietly watched something else would
// pass every leak test and prove nothing.
func TestGuardHomeWatchesTheMachineHomeByDefault(t *testing.T) {
	Convey("Given no override", t, func() {
		// Set, not assumed. A shell that exports BEADLE_TEST_GUARD_HOME —
		// which is what a developer running the probes has — would otherwise
		// turn "the default is the real home" into a test that only passes on
		// someone else's machine.
		t.Setenv(homescan.OverrideEnv, "")
		t.Setenv("HOME", "/home/u")

		Convey("Then the guard watches the home the process was given", func() {
			So(homescan.GuardHome(), ShouldEqual, "/home/u")
		})
	})
}

func TestGuardHomeTakesTheRootItIsGiven(t *testing.T) {
	Convey("Given an override", t, func() {
		Convey("Then the guard watches that tree instead", func() {
			t.Setenv("HOME", "/home/u")
			t.Setenv(homescan.OverrideEnv, t.TempDir())

			So(homescan.GuardHome(), ShouldNotEqual, "/home/u")
		})
	})
}

func TestTakeWithNoHomeWatchesNothing(t *testing.T) {
	Convey("Given a run with no HOME", t, func() {
		Convey("Then there is nothing to compare and nothing to report", func() {
			before := homescan.Take("")

			So(before.Changed(before.Recheck()), ShouldBeEmpty)
		})
	})
}

func TestAWriteInsideAnExistingDirectoryIsSeen(t *testing.T) {
	Convey("Given a skills surface that already exists", t, func() {
		home := t.TempDir()
		skills := filepath.Join(home, ".claude", "skills")
		write(t, filepath.Join(skills, "existing.json"), "{}")

		before := homescan.Take(home)

		Convey("When the suite drops a file inside it", func() {
			// The top-level name check this replaced would never see it: the
			// only entry that appears is inside a directory already there.
			write(t, filepath.Join(skills, "x"), "x")

			Convey("Then the guard names the file and its path", func() {
				So(before.Changed(before.Recheck()), ShouldResemble,
					[]string{"created  " + filepath.Join(skills, "x")})
			})
		})

		Convey("When the suite rewrites a file it did not create", func() {
			path := filepath.Join(skills, "existing.json")
			write(t, path, `{"a":1}`)

			later := time.Now().Add(time.Hour)
			if err := os.Chtimes(path, later, later); err != nil {
				t.Fatalf("chtimes: %v", err)
			}

			Convey("Then the guard names it even though nothing new was added", func() {
				So(before.Changed(before.Recheck()), ShouldResemble, []string{"changed  " + path})
			})
		})

		Convey("When the suite deletes it", func() {
			if err := os.Remove(filepath.Join(skills, "existing.json")); err != nil {
				t.Fatalf("remove: %v", err)
			}

			Convey("Then the removal is reported, not just creations", func() {
				So(before.Changed(before.Recheck()), ShouldResemble,
					[]string{"removed  " + filepath.Join(skills, "existing.json")})
			})
		})
	})
}

func TestASurfaceTheSuiteCreatesIsSeen(t *testing.T) {
	Convey("Given a home with no claude agents surface", t, func() {
		home := t.TempDir()

		before := homescan.Take(home)

		Convey("When the suite creates the whole surface", func() {
			agents := filepath.Join(home, ".claude", "agents")
			write(t, filepath.Join(agents, "a.md"), "a")

			Convey("Then the surface and the tree under it are named", func() {
				So(before.Changed(before.Recheck()), ShouldResemble, []string{
					"created  " + agents,
					"created  " + filepath.Join(agents, "a.md"),
				})
			})
		})
	})
}

// The bug this pins is one the guard paid for in the field: a suite pins
// XDG_CONFIG_HOME between the two snapshots, so resolving the roots again at the
// end pointed the second walk at the temp tree and reported every file under the
// developer's real config as removed.
func TestTheRootsAreNotResolvedAgainAtTheEnd(t *testing.T) {
	Convey("Given a snapshot taken before the suite pins its environment", t, func() {
		home := t.TempDir()
		doc := filepath.Join(home, ".config", "opencode", "opencode.json")
		write(t, doc, "{}")

		before := homescan.Take(home)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "elsewhere"))

		Convey("Then the recheck still watches where the snapshot watched", func() {
			So(before.Changed(before.Recheck()), ShouldBeEmpty)
		})
	})
}

func TestOnlyTheWatchedRootsAreReported(t *testing.T) {
	Convey("Given a suite that touched nothing beadle owns", t, func() {
		home := t.TempDir()

		before := homescan.Take(home)

		Convey("When a directory outside the watched surfaces changes", func() {
			// A live session rewrites exactly this kind of file while a suite
			// runs. Reporting it would be a guard that fires on the developer.
			write(t, filepath.Join(home, "Documents", "notes.md"), "x")
			write(t, filepath.Join(home, ".claude", "projects", "p", "chat.jsonl"), "{}")

			Convey("Then the guard stays quiet", func() {
				So(before.Changed(before.Recheck()), ShouldBeEmpty)
			})
		})

		Convey("When a watched directory's mtime moves but nothing inside it does", func() {
			skills := filepath.Join(home, ".claude", "skills")
			if err := os.MkdirAll(skills, 0o750); err != nil {
				t.Fatalf("mkdir: %v", err)
			}

			before := homescan.Take(home)

			later := time.Now().Add(time.Hour)
			if err := os.Chtimes(skills, later, later); err != nil {
				t.Fatalf("chtimes: %v", err)
			}

			Convey("Then the guard stays quiet: a directory's mtime is not a change", func() {
				So(before.Changed(before.Recheck()), ShouldBeEmpty)
			})
		})
	})
}

// The two modes, and the reason they are two: the paths a live agent writes are
// the paths beadle writes, so on a developer's machine the guard cannot tell a
// leak from a neighbour. It stops trying there and holds the run to account
// only where there is no neighbour.
func TestTheVerdictDependsOnWhoseMachineThisIs(t *testing.T) {
	Convey("Given a watched surface that moved under the suite", t, func() {
		home := t.TempDir()
		skills := filepath.Join(home, ".claude", "skills")
		write(t, filepath.Join(skills, "existing.md"), "x")

		before := homescan.Take(home)

		write(t, filepath.Join(skills, "leaked.md"), "x")

		changes := before.Changed(before.Recheck())

		Convey("Then something was found", func() {
			So(changes, ShouldNotBeEmpty)
		})

		Convey("When this is CI, where nothing else ran", func() {
			t.Setenv("CI", "true")

			Convey("Then the suite is held to account for it", func() {
				So(homescan.Judge(changes), ShouldEqual, homescan.Fail)
			})
		})

		Convey("When the run asked to be held to account anywhere", func() {
			t.Setenv(homescan.StrictEnv, "strict")

			Convey("Then it is", func() {
				So(homescan.Judge(changes), ShouldEqual, homescan.Fail)
			})
		})

		Convey("When this is a developer machine with an agent running", func() {
			t.Setenv("CI", "")
			t.Setenv(homescan.StrictEnv, "")

			Convey("Then the suite warns instead of failing", func() {
				// Not silence: the home moved and the reader is told so, in
				// words that say why it is not being held to account.
				So(homescan.Judge(changes), ShouldEqual, homescan.Warn)
			})
		})

		Convey("When the home held still", func() {
			Convey("Then no verdict turns on whose machine it is", func() {
				t.Setenv("CI", "true")
				So(homescan.Judge(nil), ShouldEqual, homescan.Clean)

				t.Setenv("CI", "")
				So(homescan.Judge(nil), ShouldEqual, homescan.Clean)
			})
		})
	})
}

func TestStrictReadsTheTwoKnobsTheOrchestratorNamed(t *testing.T) {
	Convey("Given no knobs", t, func() {
		Convey("Then a local run is not strict", func() {
			t.Setenv("CI", "")
			t.Setenv(homescan.StrictEnv, "")

			So(homescan.Strict(), ShouldBeFalse)
		})

		Convey("And CI is, in every spelling a CI system actually uses", func() {
			for _, value := range []string{"true", "TRUE", "1", "yes", " true "} {
				t.Setenv("CI", value)
				So(homescan.Strict(), ShouldBeTrue)
			}
		})

		Convey("And CI=false is not CI", func() {
			// Some local CI emulators set it to exactly this. Reading it as
			// strict would hold a developer to account for their own agent.
			t.Setenv("CI", "false")
			t.Setenv(homescan.StrictEnv, "")

			So(homescan.Strict(), ShouldBeFalse)
		})

		Convey("And CI wins over an ask for leniency", func() {
			t.Setenv("CI", "true")
			t.Setenv(homescan.StrictEnv, "warn")

			So(homescan.Strict(), ShouldBeTrue)
		})

		Convey("And the override works with no CI at all", func() {
			t.Setenv("CI", "")
			t.Setenv(homescan.StrictEnv, "STRICT")

			So(homescan.Strict(), ShouldBeTrue)
		})
	})
}

func TestAVerdictSaysItsNameWhenAnAssertionFails(t *testing.T) {
	Convey("Given the three verdicts", t, func() {
		// Otherwise a failure reads "Expected: 2, Actual: 1", and the reader
		// has to remember which number was which.
		So(homescan.Clean.String(), ShouldEqual, "Clean")
		So(homescan.Warn.String(), ShouldEqual, "Warn")
		So(homescan.Fail.String(), ShouldEqual, "Fail")
	})
}

// The warning is the whole of what a lenient run has to say, so what it has to
// contain is pinned: every path that moved, the reason this run is not being
// blamed for them, and the switch that would blame it. A warning that names
// none of the three is a warning nobody can act on.
func TestTheWarningNamesTheChangesAndHowToHoldTheRunToAccount(t *testing.T) {
	Convey("Given two changes and a lenient run", t, func() {
		lines := homescan.WarnLines([]string{
			"created  /home/u/.claude.json",
			"changed  /home/u/.claude/skills/synced/abc/manifest.json",
		})

		Convey("Then it names every change in full", func() {
			So(lines, ShouldContain, "WARNING real home: created  /home/u/.claude.json")
			So(lines, ShouldContain,
				"WARNING real home: changed  /home/u/.claude/skills/synced/abc/manifest.json")
		})

		Convey("And it says the run is not to blame, and why that is not a clean bill", func() {
			// The neighbour reading is the one a developer will reach for, and
			// the reason to leave it there has to be stated or the warning
			// reads as a shrug.
			So(strings.Join(lines, "\n"), ShouldContainSubstring, "not held to account")
			So(strings.Join(lines, "\n"), ShouldContainSubstring, "not evidence of a leak")
		})

		Convey("And it says what would make it a failure", func() {
			So(strings.Join(lines, "\n"), ShouldContainSubstring, homescan.StrictEnv+"=strict")
			So(strings.Join(lines, "\n"), ShouldContainSubstring, "CI=true")
		})

		Convey("And every line is marked, so a scrolled log is still readable", func() {
			for _, line := range lines {
				So(line, ShouldStartWith, "WARNING")
			}
		})
	})
}
