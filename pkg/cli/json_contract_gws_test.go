package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/verger/pkg/verger"
)

// jsonCommands walks the whole cobra tree and returns every command a user can
// reach, paired with the document its --json form is named after, or "" for a
// command that has none. `beadle help` hides a command that is not runnable, so
// the walk asks cobra rather than parsing help text.
func jsonCommands(t *testing.T) map[string]string {
	t.Helper()

	root := newRootCmd(testOptions())
	out := map[string]string{}

	var walk func(*cobra.Command)

	walk = func(cmd *cobra.Command) {
		if cmd.Runnable() {
			// TrimPrefix alone leaves the space that separated the words, and a
			// key with a leading space matches nothing — which is how a table of
			// arguments silently stops applying to half the tree.
			path := strings.TrimSpace(strings.TrimPrefix(cmd.CommandPath(), "beadle"))
			out[path] = jsonSchemaName(cmd)
		}

		for _, sub := range cmd.Commands() {
			walk(sub)
		}
	}

	walk(root)

	return out
}

// jsonArgs are the arguments a command needs before it can answer at all. A
// command that takes a required argument is not reachable bare, and cobra
// validates arguments BEFORE the --json check, so a walk that omits them would
// measure cobra's argument validator instead of the contract. Only the minimum
// is supplied, and only where the signature demands it.
// jsonArgs are the arguments a command needs before it can answer at all, keyed
// by the path the walk reports. A command that takes a required argument is not
// reachable bare, and cobra validates arguments BEFORE the --json check, so a
// walk that omits them would measure cobra's argument validator instead of the
// contract. Only the minimum is supplied, and only where the signature demands
// it — a value that names nothing real, because the question is what the command
// does with --json, not whether the run succeeds.
var jsonArgs = map[string][]string{
	"agents disable":        {"claude"},
	"agents enable":         {"claude"},
	"agents mode":           {"claude", "rules", "sync"},
	"bundles disable":       {"claude"},
	"bundles enable":        {"claude"},
	"conflicts":             {"none"},
	"daemon install":        {},
	"daemon uninstall":      {},
	"export agent-plugins":  {},
	"explain":               {"alpha"},
	"history":               {"rules"},
	"hooks add":             {"--plugin", "acme/tool"},
	"hooks approve":         {"--plugin", "acme/tool"},
	"hooks list":            {},
	"hooks revoke":          {"--plugin", "acme/tool"},
	"hooks rm":              {"beadle-notify"},
	"kinds disable":         {"rules"},
	"kinds enable":          {"rules"},
	"plugins canon disable": {"canon"},
	"plugins canon enable":  {"canon"},
	"plugins eject":         {},
	"plugins install":       {"acme/tool"},
	"plugins pin":           {"acme/tool", "1.0.0"},
	"plugins pins":          {},
	"plugins remove":        {"acme/tool"},
	"plugins unpin":         {"acme/tool"},
	"project disable":       {"."},
	"project enable":        {"."},
	"project forget":        {"."},
	"project status":        {},
	"resolve":               {"none"},
	"restore":               {"rules"},
	"rulings forget":        {"none"},
	"rulings show":          {"none"},
	"rulings trust":         {"none"},
	"secrets prune":         {},
	"secrets rm":            {"token"},
	"secrets set":           {"token", "value"},
	"skills adopt":          {"alpha"},
	"skills seed":           {},
	"skills unadopt":        {"alpha"},
}

// TestEveryCommandAnswersJSONOrRefuses is the machine contract, walked.
//
// `--json` is global, so every command is asked whether it can answer it. A
// command that can prints one object whose first field is the envelope. A
// command that cannot says so and exits 2, naming the commands that can.
//
// The case that must not exist is the third one: printing the human table while
// the flag is set. It reaches a script as a `jq` parse error with nothing
// pointing at the flag that caused it, and no test of the documents themselves
// would ever notice — which is why the check is a walk rather than a list.
func TestEveryCommandAnswersJSONOrRefuses(t *testing.T) {
	Convey("Given every command in the tree, asked for its machine-readable form", t, func() {
		commands := jsonCommands(t)
		So(commands, ShouldNotBeEmpty)

		for path, name := range commands {
			shown := strings.TrimSpace(path)
			if shown == "" {
				shown = "(no verb)"
			}

			label := "beadle " + shown + " --json"

			Convey("Then "+label+" answers", func() {
				gwsHome(t)

				if _, initErr := runCLI(t, "init"); initErr != nil {
					t.Fatalf("init: %v", initErr)
				}

				// The path first, then whatever the command needs, then the flag:
				// dropping the path would ask the ROOT the question and every command
				// would look like the one that has no form.
				args := append(strings.Fields(path), jsonArgs[path]...)
				args = append(args, "--json")
				out, err := gwsRun(t, args...)

				if name == "" {
					// No document, so the run must not succeed quietly. Two loud
					// failures count and neither is a silent ignore: the refusal
					// itself, and an argument or flag error that cobra raised
					// first — the walk does not get to order those two, and
					// cmd/beadle maps both to exit 2 anyway.
					So(err, ShouldNotBeNil)

					if errors.As(err, new(*verger.UsageError)) {
						So(err.Error(), ShouldContainSubstring, "no machine-readable form")
						So(err.Error(), ShouldContainSubstring, "beadle status")
					}

					return
				}

				// A command that failed for its own reasons — a skill that is not
				// in the canon, a host that is not installed — has not answered the
				// question, and making the walk depend on every command's happy
				// path would make it a test of the fixtures instead. What it must
				// never do is succeed while printing something else: err == nil
				// with a table instead of the declared document is precisely the
				// silent ignore this walk exists to catch, and every command whose
				// document matters has its own test for the content.
				if err != nil {
					return
				}

				var doc map[string]any

				So(json.Unmarshal([]byte(out), &doc), ShouldBeNil)

				schema, ok := doc["schema"].(map[string]any)
				So(ok, ShouldBeTrue)
				So(schema["name"], ShouldEqual, name)
				So(schema["version"], ShouldEqual, float64(SchemaVersion))
			})
		}
	})
}

// TestEveryJSONDocumentNameIsDistinctAndPrefixed pins the two ways the envelope
// can lie at once: two commands answering under one name, and a name that is
// not beadle's to give. A consumer switches on `schema.name`, so a duplicate
// makes the switch ambiguous and a foreign name makes it a guess.
func TestEveryJSONDocumentNameIsDistinctAndPrefixed(t *testing.T) {
	Convey("Given the documents the tree declares", t, func() {
		seen := map[string]string{}

		for path, name := range jsonCommands(t) {
			if name == "" {
				continue
			}

			shown := strings.TrimSpace(path)
			if shown == "" {
				shown = "(no verb)"
			}

			So(name, ShouldStartWith, "beadle.")
			So(seen[name], ShouldBeEmpty)
			seen[name] = "beadle " + shown
		}

		So(seen, ShouldNotBeEmpty)
	})
}

// TestTheRefusalNamesTheCommandsThatCan is the half of the refusal a script
// cannot act on without: a bare "no" leaves the caller to know beadle's command
// list, which is exactly the knowledge `--json` was supposed to remove.
func TestTheRefusalNamesTheCommandsThatCan(t *testing.T) {
	Convey("Given a command with no machine-readable form", t, func() {
		gwsHome(t)

		Convey("When it is asked for one", func() {
			_, err := gwsRun(t, "diff", "--json")
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "no machine-readable form")

			Convey("Then the refusal lists the commands that have one", func() {
				So(err.Error(), ShouldContainSubstring, "beadle status")
				So(err.Error(), ShouldContainSubstring, "beadle kinds")
			})

			Convey("And stdout carries no document, so a pipe sees the error and nothing else", func() {
				// The shared harness merges stderr into the same buffer, which
				// would make this leaf assert that cobra does not report errors
				// at all. Run it with the streams apart: the claim is about the
				// document channel, and the document channel is stdout.
				var out, errOut bytes.Buffer

				root := newRootCmd(testOptions())
				root.SetOut(&out)
				root.SetErr(&errOut)
				root.SetArgs([]string{"diff", "--json"})
				root.SilenceUsage = true

				_ = root.ExecuteContext(t.Context())

				So(strings.TrimSpace(out.String()), ShouldBeEmpty)
				So(errOut.String(), ShouldContainSubstring, "no machine-readable form")
			})
		})
	})
}

// TestTheGlobalFlagReachesTheSameAnswer pins both spellings, because the flag is
// documented as working on either side of the verb and the two take different
// routes through cobra's parser.
func TestTheGlobalFlagReachesTheSameAnswer(t *testing.T) {
	Convey("Given --json before the verb and after it", t, func() {
		gwsHome(t)

		if _, initErr := runCLI(t, "init"); initErr != nil {
			t.Fatalf("init: %v", initErr)
		}

		Convey("Then a command with a form answers the same document both ways", func() {
			before, errBefore := gwsRun(t, "--json", "kinds")

			after, errAfter := gwsRun(t, "kinds", "--json")

			So(errBefore, ShouldBeNil)
			So(errAfter, ShouldBeNil)
			So(before, ShouldEqual, after)
		})

		Convey("And a command without one is refused both ways", func() {
			_, errBefore := gwsRun(t, "--json", "diff")

			_, errAfter := gwsRun(t, "diff", "--json")

			So(errBefore, ShouldNotBeNil)
			So(errAfter, ShouldNotBeNil)
			So(errBefore.Error(), ShouldEqual, errAfter.Error())
		})
	})
}
