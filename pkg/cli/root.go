package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	texttemplate "text/template"

	"github.com/odiumuniverse/verger/pkg/verger"
	"github.com/spf13/cobra"
	"github.com/vmkteam/embedlog"

	"github.com/odiumuniverse/beadle/pkg/daemon"
	"github.com/odiumuniverse/beadle/pkg/secret"
)

type Options struct {
	Version string

	// DisableAutoEnable turns off the two unattended defaults: the bundle
	// attempt and the init-time project defaults. A test that runs against a
	// temp home sets it, so a run cannot reach the machine the suite is on —
	// and, because it is a per-app field, one test can no longer change what
	// another test's run does.
	DisableAutoEnable bool

	// EnableBundleAttempt and EnableProjectDefaults turn ONE of them back on
	// for this run, for the tests that are about that default. They are
	// separate because the two defaults are separate products: a test about
	// the bundle attempt has no business enabling the project files.
	EnableBundleAttempt   bool
	EnableProjectDefaults bool

	// DaemonInstall and DaemonCheck replace the two process calls the daemon
	// commands make. nil means the real ones. A test passes a recorder here
	// instead of assigning a package variable, which is what made the suite
	// order-dependent under `go test -count=3`.
	DaemonInstall daemon.Runner
	DaemonCheck   secret.Runner

	// DaemonTemporaryHome replaces the temporary-root check the installer
	// consults. nil means the real one. A test that runs against a temp home
	// needs the gate lifted, and lifting it for the whole process meant every
	// later test in that process saw the lift too.
	DaemonTemporaryHome func(string) bool
}

func Execute(opts Options) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := newRootCmd(opts)
	root.SilenceUsage = true
	root.SilenceErrors = true
	// A flag the user got wrong, or the wrong number of arguments, is class 2,
	// not a crash. Both are wrapped in verger's typed UsageError so
	// cmd/beadle's classifier recognises them without matching on a message.
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &verger.UsageError{Cause: err}
	})
	markUsageErrors(root)

	return root.ExecuteContext(ctx)
}

func newRootCmd(opts Options) *cobra.Command {
	a := &app{
		logger:              embedlog.NewLogger(false, false),
		errOut:              os.Stderr,
		bundleAutoEnable:    !opts.DisableAutoEnable || opts.EnableBundleAttempt,
		projectAutoEnable:   !opts.DisableAutoEnable || opts.EnableProjectDefaults,
		daemonInstall:       opts.DaemonInstall,
		daemonCheck:         opts.DaemonCheck,
		daemonTemporaryHome: opts.DaemonTemporaryHome,
	}

	return newRootCmdWithApp(a, opts)
}

// carryDown hands a root flag's value to the command that shadows it. A
// command's own flag of the same name wins during parsing, so without this a
// global `beadle --json status` would be parsed on the root and then read as
// the command's default - and the missing document would look like a bug in
// the command rather than in the flag. A value the user gave on the command
// itself is left alone, so an explicit `--json=false` still wins.
func carryDown(cmd *cobra.Command, name string, on bool) {
	if !on {
		return
	}

	flag := cmd.Flags().Lookup(name)
	if flag == nil || flag.Changed {
		return
	}

	_ = flag.Value.Set("true")
	flag.Changed = true
}

// markUsageErrors wraps every command's argument validator so an arity or
// argument error arrives as a typed usage error. Cobra has no global hook for
// this, and wrapping each call site separately would let a new command forget.
func markUsageErrors(cmd *cobra.Command) {
	if cmd.Args != nil {
		cmd.Args = usageArgs(cmd.Args)
	}

	for _, sub := range cmd.Commands() {
		markUsageErrors(sub)
	}
}

// usageArgs turns a positional-argument validator's error into a typed usage
// error, leaving the original message intact for the user.
func usageArgs(validator cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := validator(cmd, args); err != nil {
			return &verger.UsageError{Cause: err}
		}

		return nil
	}
}

// newRootCmdWithApp builds the command tree around an existing app, so a test
// can inject the plugin manager the way the CLI injects it in production.
func newRootCmdWithApp(a *app, opts Options) *cobra.Command {
	var verbose, logJSON, jsonOut, assumeYes bool

	root := &cobra.Command{
		Use:   "beadle",
		Short: "One configuration for every AI coding agent",
		Long: "beadle keeps the rules, MCP servers, skills and permissions of your AI coding\n" +
			"agents (Claude Code, OpenCode, Gemini CLI, Cursor, ...) in sync through a vault\n" +
			"you own. Change them in any agent: the others follow. Real files, 3-way merges,\n" +
			"no symlinks; conflicts wait for you instead of being guessed.\n\n" +
			"Are you an AI agent? Run `beadle guide`.",
		Version: opts.Version,
		// With no Args, cobra falls back to its untyped legacyArgs and an
		// unknown command exits as a plain error. Naming the validator here
		// routes that message through usageArgs, so "beadle nosuchcommand" is
		// class 2 like every other usage error. The same trick verger uses.
		Args: usageArgs(cobra.NoArgs),
		// Bare "beadle" prints the help. The RunE is what makes the command
		// runnable, and cobra skips argument validation on a command it is
		// not going to run — without it, Args above never fires and an
		// unknown command exits 0.
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			a.errOut = cmd.ErrOrStderr()

			// The dev logger writes INFO to STDOUT at debug level, and stdout is
			// the channel a --json command's document travels on: one log line
			// ahead of it makes `beadle doctor --json | jq` fail at character 0.
			// stdout is a data channel - a --json document, a table, a pipe -
			// and the dev logger writes INFO to it, so one log line breaks
			// every consumer that pipes the command. The root logger is
			// therefore always the split one: errors on stderr, info only when
			// -v asks for it. A notice that must always be visible is written
			// to errOut as a plain line, which is what that field is for.
			//
			// embedlog v0.1.3 hardcodes os.Stdout in both constructors and keeps
			// its writer unexported, so INFO cannot be routed to stderr rather
			// than dropped; that needs an upstream release.
			a.logger = embedlog.NewLogger(verbose && !logJSON, logJSON)

			// A command that declares its own --json or -y shadows the root's
			// flag of the same name, so `beadle --json status` parses the flag
			// on the root and then reads the command's own default: the
			// document would be missing and nothing would say why. Carrying
			// the root's value down closes that, and an explicit value on the
			// command still wins, so `--json=false` after a global --json is
			// not overridden.
			carryDown(cmd, "json", jsonOut)
			carryDown(cmd, "yes", assumeYes)

			// --json is global, so every command is asked the question whether it
			// can answer it. A command that has no document says so and exits 2,
			// here rather than in each command: the check is one fact about the
			// tree, and twenty copies of it would be twenty places to forget one.
			// Silently printing the human table is the one answer a JSON
			// consumer cannot use — `beadle hooks --json | jq` fails at character
			// zero with nothing pointing at the flag.
			if asked, _ := cmd.Flags().GetBool("json"); asked && jsonSchemaName(cmd) == "" {
				return noJSONForm(cmd, cmd.Root())
			}

			return nil
		},
	}

	root.PersistentFlags().StringVar(&a.vaultPath, "vault", "", "vault root (default: $BEADLE_HOME or ~/.beadle)")
	root.PersistentFlags().BoolVar(&verbose, "verbose", false, "enable info-level logs")
	root.PersistentFlags().BoolVar(&logJSON, "log-json", false, "log in JSON format")
	// --json and -y are global: a consumer that pipes beadle's output should
	// not have to know which verb prints it, and a script that answers "yes"
	// should not have to learn the spelling of each command. A command that
	// declares its own flag of the same name still shadows this one, and
	// carryDown hands it the value.
	root.PersistentFlags().BoolVar(&jsonOut, "json", false, "print the machine-readable document")
	root.PersistentFlags().BoolVarP(&assumeYes, "yes", "y", false, "answer yes to the prompts this command asks")

	root.AddCommand(
		a.newInitCmd(),
		a.newMigrateCmd(),
		a.newGuideCmd(),
		a.newStatusCmd(),
		a.newSyncCmd(),
		a.newPullCmd(),
		a.newPushCmd(),
		a.newDiffCmd(),
		a.newConflictsCmd(),
		a.newResolveCmd(),
		a.newRulingsCmd(),
		a.newDoctorCmd(),
		a.newHistoryCmd(),
		a.newRestoreCmd(),
		a.newHealCmd(),
		a.newAgentsCmd(),
		a.newKindsCmd(),
		a.newPluginsCmd(),
		a.newHooksCmd(),
		a.newBundlesCmd(),
		a.newSkillsCmd(),
		a.newExportCmd(),
		a.newExplainCmd(),
		a.newWatchCmd(),
		a.newDaemonCmd(),
		a.newSecretsCmd(),
		newVersionCmd(),
		a.newProjectCmd(),
	)

	return root
}

// newVersionCmd answers `beadle version`, so the tool answers to both shapes a
// user reaches for: the subcommand and -v/--version. The subcommand renders the
// root's own version template rather than a string of its own, so the two
// cannot drift apart when the format changes.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the beadle version",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			root := cmd.Root()

			tmpl, err := texttemplate.New("version").Parse(root.VersionTemplate())
			if err != nil {
				return fmt.Errorf("beadle version: %w", err)
			}

			var buf bytes.Buffer
			if err := tmpl.Execute(&buf, root); err != nil {
				return fmt.Errorf("beadle version: %w", err)
			}

			_, err = fmt.Fprint(cmd.OutOrStdout(), buf.String())

			return err
		},
	}
}
