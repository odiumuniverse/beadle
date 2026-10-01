package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/cli"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/hostcli"
	"github.com/odiumuniverse/beadle/pkg/lock"
	"github.com/odiumuniverse/beadle/pkg/secret"
	"github.com/odiumuniverse/beadle/pkg/state"
	"github.com/odiumuniverse/verger/pkg/apply"
	"github.com/odiumuniverse/verger/pkg/exitcode"
	vergerlock "github.com/odiumuniverse/verger/pkg/lock"
	"github.com/odiumuniverse/verger/pkg/verger"
)

func TestClassifyNilIsOK(t *testing.T) {
	Convey("Given no error", t, func() {
		So(classify(nil), ShouldEqual, exitcode.OK)
	})
}

// The enumeration of every situation beadle can name. A row is one real
// error value built by the package that produces it — never a string
// fabricated here — so the table cannot drift from the code: if a constructor
// changes shape, this stops compiling.
func TestEveryNamedSituationHasItsOwnClass(t *testing.T) {
	Convey("Given one real error per situation beadle can name", t, func() {
		table := []struct {
			name string
			err  error
			want int
		}{
			// nil: the command did what was asked. The user sees nothing.
			{name: "nothing went wrong", err: nil, want: exitcode.OK},

			// busy lock: another beadle is writing right now. The user sees
			// "vault is busy" and waits, or stops the other process.
			{name: "vault busy", err: lock.ErrBusy, want: exitcode.Conflict},

			// open conflicts: beadle applied what it could and stopped at the
			// user's own files. The user sees "N open conflict(s) … run
			// `beadle conflicts`".
			{name: "open conflicts", err: engine.OpenConflictsError{Conflicts: 2}, want: exitcode.Conflict},

			// keyring: a secret cannot be stored on this machine. The user
			// sees "no keyring available" and provides one, or opts out.
			{name: "keyring unavailable", err: secret.ErrKeyringUnavailable, want: exitcode.Consent},

			// a question the tool could not ask. verger moved this sentinel from
			// the conflict class to the consent class in v0.1.2, on the grounds that
			// the user's next move is to answer it or pass -y, and telling a script
			// "conflict" told it the run failed for a reason it cannot act on. Left
			// out, a confirmation required inside the library surfaced through a
			// beadle command fell to the default branch and exited 1.
			{name: "verger asked and was not answered", err: apply.ErrConfirmationRequired, want: exitcode.Consent},

			// host CLI missing: the host cannot be reached. The user sees
			// "host not found" and installs it, or runs without that host.
			{name: "host cli not found", err: hostcli.ErrNotFound, want: exitcode.HostUnavailable},

			// no vault: a missing step. The user sees "run beadle init".
			{name: "vault not initialized", err: cli.ErrVaultNotInitialized, want: exitcode.Usage},

			// unknown agent: a flag the user got wrong. The user sees the
			// agent name and the agents that exist.
			{name: "agent not configured", err: agent.ErrNotConfigured, want: exitcode.Usage},

			// verger's own typed usage error, raised by pkg/cli for every bad
			// flag or argument count. The user sees cobra's own message.
			{name: "verger usage error", err: &verger.UsageError{Cause: errors.New("unknown flag --nope")}, want: exitcode.Usage},

			// refusal: a beadle rule says no. The user sees the code and the
			// reason, e.g. "risky-change: … re-run with --allow-risky".
			{name: "policy refusal", err: engine.NewRefusalError("risky-change", "re-run with --allow-risky"), want: exitcode.Policy},

			// a vault document from a newer beadle. The user sees which
			// document, both versions, and "upgrade beadle" — never a rewrite.
			{
				name: "config from a newer beadle",
				err:  &config.SchemaNewerError{Path: "/h/.beadle/config.json", Found: config.CurrentVersion + 1, Supported: config.CurrentVersion},
				want: exitcode.SchemaNewer,
			},
			{
				name: "state from a newer beadle",
				err:  &state.SchemaNewerError{Path: "/h/.beadle/state.json", Found: state.CurrentVersion + 1, Supported: state.CurrentVersion},
				want: exitcode.SchemaNewer,
			},
			{
				name: "verger spec from a newer verger",
				err:  &vergerlock.SchemaNewerError{Path: "/h/.verger/spec.json", Found: 99, Supported: 3},
				want: exitcode.SchemaNewer,
			},

			// The one row that reaches the default branch. exitcode.Unexpected
			// is 1 in the shared table, not 8: an error nobody named. The user
			// sees "error: boom" and has to read it — which is why every
			// situation above had to be given a number of its own.
			{name: "unnamed error", err: errors.New("boom"), want: exitcode.Unexpected},
		}

		for _, item := range table {
			Convey("Then "+item.name+" is "+exitcode.Name(item.want), func() {
				So(classify(item.err), ShouldEqual, item.want)
			})
		}

		// The claim behind the table: every situation beadle can name has a
		// class, so a named situation never lands in the default branch.
		// Proved by walking the classes, not by asserting it.
		Convey("Then every class from ok to schema newer is reached by a real error", func() {
			seen := map[int]bool{}

			for _, item := range table {
				seen[classify(item.err)] = true
			}

			for class := exitcode.OK; class <= exitcode.SchemaNewer; class++ {
				So(seen[class], ShouldBeTrue)
			}
		})
	})
}

func TestClassifySeesThroughWrapping(t *testing.T) {
	Convey("Given a sentinel wrapped twice", t, func() {
		wrapped := fmt.Errorf("sync: %w", fmt.Errorf("lock: %w", lock.ErrBusy))

		Convey("Then the class is the inner one", func() {
			So(classify(wrapped), ShouldEqual, exitcode.Conflict)
		})
	})
}

// The classifier's whole job is to survive wrapping: every command wraps on
// the way out, so a class found only on a bare error would never be seen by
// the binary. Driven from the errors the real loaders produce, not from
// hand-built structs, so the test breaks if Load ever stops returning the
// type.
func TestSchemaNewerSurvivesWrappingFromTheRealLoaders(t *testing.T) {
	Convey("Given a config.json and a state.json written by a newer beadle", t, func() {
		dir := t.TempDir()
		configPath := filepath.Join(dir, config.FileName)
		statePath := filepath.Join(dir, state.FileName)

		writeJSON(t, configPath, `{"version":`+strconv.Itoa(config.CurrentVersion+1)+`}`)

		writeJSON(t, statePath, `{"version":`+strconv.Itoa(state.CurrentVersion+1)+`}`)

		_, configErr := config.Load(configPath)
		_, stateErr := state.Load(statePath)

		Convey("Then each loader refuses with its own typed error", func() {
			newerConfig, ok := config.IsSchemaNewer(configErr)
			So(ok, ShouldBeTrue)
			So(newerConfig.Found, ShouldEqual, config.CurrentVersion+1)
			So(newerConfig.Supported, ShouldEqual, config.CurrentVersion)

			newerState, ok := state.IsSchemaNewer(stateErr)
			So(ok, ShouldBeTrue)
			So(newerState.Found, ShouldEqual, state.CurrentVersion+1)
			So(newerState.Supported, ShouldEqual, state.CurrentVersion)
		})

		Convey("Then each classifies as 7 bare", func() {
			So(classify(configErr), ShouldEqual, exitcode.SchemaNewer)
			So(classify(stateErr), ShouldEqual, exitcode.SchemaNewer)
		})

		Convey("Then each classifies as 7 through the wrapping a command adds", func() {
			// The wrapping `beadle migrate` effectively performs on its way out.
			So(classify(fmt.Errorf("migrate config: %w", configErr)), ShouldEqual, exitcode.SchemaNewer)
			So(classify(fmt.Errorf("migrate state: %w", stateErr)), ShouldEqual, exitcode.SchemaNewer)
		})

		Convey("Then the message names the document and both versions", func() {
			So(configErr.Error(), ShouldContainSubstring, config.FileName)
			So(configErr.Error(), ShouldContainSubstring, "version "+strconv.Itoa(config.CurrentVersion+1))
			So(configErr.Error(), ShouldContainSubstring, "upgrade beadle")

			So(stateErr.Error(), ShouldContainSubstring, state.FileName)
			So(stateErr.Error(), ShouldContainSubstring, "version "+strconv.Itoa(state.CurrentVersion+1))
			So(stateErr.Error(), ShouldContainSubstring, "upgrade beadle")
		})
	})
}

// A current document must not be mistaken for a newer one: the whole point
// of class 7 is that it means "stop, upgrade", and a false 7 on a healthy
// vault would tell users to upgrade a binary that is already current.
func TestCurrentDocumentsAreNotSchemaNewer(t *testing.T) {
	Convey("Given a vault whose documents are at the current version", t, func() {
		dir := t.TempDir()
		configPath := filepath.Join(dir, config.FileName)
		statePath := filepath.Join(dir, state.FileName)

		So(config.Default().Save(configPath), ShouldBeNil)
		So(state.New().Save(statePath), ShouldBeNil)

		Convey("Then loading them raises no schema-newer error", func() {
			_, configErr := config.Load(configPath)
			_, stateErr := state.Load(statePath)

			_, ok := config.IsSchemaNewer(configErr)
			So(ok, ShouldBeFalse)
			_, ok = state.IsSchemaNewer(stateErr)
			So(ok, ShouldBeFalse)
		})
	})
}

func writeJSON(t *testing.T, path, body string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// The point of sharing verger's constants: the same situation must produce the
// same number whichever tool reported it.
func TestBothToolsAgreeOnTheSharedClasses(t *testing.T) {
	Convey("Given the classes beadle reuses", t, func() {
		Convey("Then they are the same integers verger publishes", func() {
			So(exitcode.OK, ShouldEqual, 0)
			So(exitcode.Usage, ShouldEqual, 2)
			So(exitcode.Conflict, ShouldEqual, 3)
			So(exitcode.Policy, ShouldEqual, 4)
			So(exitcode.Consent, ShouldEqual, 5)
			So(exitcode.HostUnavailable, ShouldEqual, 6)
			So(exitcode.SchemaNewer, ShouldEqual, 7)
		})

		Convey("Then a keyring error is the same class in both", func() {
			// verger maps secret.ErrKeyringUnavailable to 5 in pkg/exitcode;
			// beadle maps the very same sentinel type to the same constant.
			So(classify(secret.ErrKeyringUnavailable), ShouldEqual, exitcode.Consent)
		})
	})
}

// engine.RefusalError is the one beadle error that carries a machine code, so
// it is the one that must classify to a class rather than fall through to
// "unexpected". The test drives it through wrapping, because a command that
// reports a refusal usually wraps it on the way out.
func TestPolicyRefusalIsClassFour(t *testing.T) {
	Convey("Given a policy refusal", t, func() {
		refusal := engine.NewRefusalError("vault-is-readonly", "the vault is read-only")

		Convey("Then it is a policy refusal, not an unexpected error", func() {
			So(classify(refusal), ShouldEqual, exitcode.Policy)
		})

		Convey("Then the class survives wrapping", func() {
			wrapped := fmt.Errorf("sync kind=%s: %w", "rules", refusal)
			So(classify(wrapped), ShouldEqual, exitcode.Policy)
		})

		Convey("Then wrapping twice still classifies", func() {
			deep := fmt.Errorf("plugins install: %w", fmt.Errorf("apply: %w", refusal))
			So(classify(deep), ShouldEqual, exitcode.Policy)
		})

		Convey("Then the stable code is reachable for a caller that wants the rule", func() {
			So(refusalCode(refusal), ShouldEqual, "vault-is-readonly")
			So(refusalCode(fmt.Errorf("wrapped: %w", refusal)), ShouldEqual, "vault-is-readonly")
		})

		Convey("Then a non-refusal has no code and does not claim the class", func() {
			So(refusalCode(errors.New("boom")), ShouldBeEmpty)
			So(classify(errors.New("boom")), ShouldEqual, exitcode.Unexpected)
		})
	})
}

// A refusal is a rule speaking, so it outranks the state it produced: one
// error must not be classified as a conflict or a usage problem just because
// it also mentions one.
func TestRefusalOutranksTheStateItProduced(t *testing.T) {
	Convey("Given a refusal that also looks like a conflict", t, func() {
		refusal := engine.NewRefusalError("host-policy-denied", "the host forbids it")

		Convey("Then the class is policy", func() {
			So(classify(refusal), ShouldEqual, exitcode.Policy)
			So(exitcode.Policy, ShouldEqual, 4)
		})
	})
}

// A command pointed at a vault that was never created is missing a
// precondition, the way `git` outside a repository is — not a crash and not a
// conflict. The class is usage (2), and the message has to name the command
// that fixes it, because the exit code is all a script sees and the hint is
// all a human gets.
func TestUninitializedVaultIsUsageAndNamesTheFix(t *testing.T) {
	Convey("Given a home with no vault", t, func() {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("BEADLE_HOME", filepath.Join(home, ".beadle"))
		// Execute reads the command line the way the binary does, so the test
		// sets it rather than reaching past the entry point into the command
		// tree: the class and the hint are the binary's contract.
		previous := os.Args
		os.Args = []string{"beadle", "sync"}

		t.Cleanup(func() { os.Args = previous })

		err := cli.Execute(cli.Options{Version: "test", DisableAutoEnable: true})

		Convey("Then the command fails as a usage error", func() {
			So(err, ShouldNotBeNil)
			So(classify(err), ShouldEqual, exitcode.Usage)
		})

		Convey("And the message names the command that creates the vault", func() {
			So(err.Error(), ShouldContainSubstring, "run beadle init")
		})
	})
}

// verger's HandsOffError is the class that is easiest to lose and hardest to
// notice losing: a cell the run refused to write because the file on disk is the
// user's own. Without the mapping it falls through to Unexpected (1), and
// Unexpected is the one number a script cannot act on — it says "a bug", and the
// user's next move is `--force`, not a bug report.
//
// Driven through every shape a command actually produces on the way out: bare,
// wrapped by fmt, and joined by errors.Join. The last one is the reason this is a
// test and not a line in the classifier — a hand-built type is found by any
// errors.As, but a join is where a classifier written with a type switch instead
// of errors.As stops seeing the type at all.
func TestHandsOffIsAConflictThroughEveryWrapping(t *testing.T) {
	Convey("Given verger's hands-off refusal in each shape a command produces", t, func() {
		handsOff := &verger.HandsOffError{Cells: []string{"acme/tool@claude"}}

		for _, shape := range []struct {
			name string
			err  error
		}{
			{"bare", handsOff},
			{"wrapped by fmt", fmt.Errorf("plugins install: %w", handsOff)},
			{"wrapped twice", fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", handsOff))},
			{"joined with another error", errors.Join(errors.New("a second failure"), handsOff)},
			{"joined the other way round", errors.Join(handsOff, errors.New("a second failure"))},
		} {
			Convey("Then "+shape.name+" is a conflict, not a bug", func() {
				So(classify(shape.err), ShouldEqual, exitcode.Conflict)
				So(exitcode.Name(classify(shape.err)), ShouldEqual, "conflict")
				So(classify(shape.err), ShouldNotEqual, exitcode.Unexpected)
			})
		}
	})

	Convey("Given the message a user reads", t, func() {
		err := fmt.Errorf("plugins install: %w", &verger.HandsOffError{Cells: []string{"acme/tool@claude"}})

		Convey("Then the command it names is the user's next move", func() {
			So(err.Error(), ShouldContainSubstring, "--force")
		})
	})
}
