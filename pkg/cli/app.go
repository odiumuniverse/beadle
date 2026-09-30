package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/vmkteam/embedlog"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/daemon"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/secret"
	"github.com/odiumuniverse/beadle/pkg/vault"
	"github.com/odiumuniverse/beadle/pkg/vergerx"
	"github.com/odiumuniverse/verger/pkg/verger"
)

// ErrVaultNotInitialized reports that the vault a command was pointed at does
// not exist yet. It is a sentinel rather than a bare formatted error because
// the exit classifier has to recognise it: "run beadle init" is the user
// missing a step, which is a usage class, while an unrecognised error is
// classified as unexpected — a crash class for a condition beadle describes
// in plain words and expects.
var ErrVaultNotInitialized = errors.New("vault is not initialized")

type app struct {
	logger    embedlog.Logger
	vaultPath string
	// execFix runs one `doctor --fix` step. It is a field so a test drives it
	// with a recorder: without it a test of --fix runs the real installer, and
	// the first version of that test did, hanging the package for ten
	// minutes. nil means the real executor.
	execFix func(ctx context.Context, cmd *cobra.Command, step []string) error
	// plugins is the one plugin-manager client this process opened. It is
	// built on first use and reused by every command, so the library keeps a
	// single client, a single lease and a single store per process.
	plugins *vergerx.Client
	// pluginsErr is the failure from opening that client, kept so a second
	// command in the same process does not retry a hopeless open.
	pluginsErr error
	// managerOverride replaces the plugin manager a test injects; production
	// never sets it, and the engine always gets the real client.
	managerOverride engine.PluginManager
	// bundleAutoEnable and projectAutoEnable are the unattended defaults, on
	// for every real run and set through Options by a test that must not
	// reach the developer's machine.
	bundleAutoEnable  bool
	projectAutoEnable bool
	// daemonInstall and daemonCheck are the two process calls the daemon
	// commands make, as fields for the same reason execFix and managerOverride
	// are: a test drives them with a recorder instead of reaching the machine.
	daemonInstall daemon.Runner
	daemonCheck   secret.Runner
	// daemonTemporaryHome is the temporary-root check the daemon installer
	// consults, as a field for the reason temporaryHome() documents.
	daemonTemporaryHome func(string) bool
	// errOut is the command's stderr, set on every run. Migration notes go
	// here as plain lines: the logger may be level-filtered (--log-json
	// without --verbose drops info), while a one-time default flip must
	// always be visible, and stdout stays machine-readable.
	errOut io.Writer
}

// The unattended defaults are fields on the app, not package variables, and
// `Options` is how a caller turns one off. They used to be globals that a test
// assigned directly, which made the suite's behaviour depend on which test ran
// before it: under `go test -count=3` the same test passed once and failed on
// its second pass, with the only difference being state another test had left
// in the process. An app that is told what to do cannot be reconfigured by a
// neighbour.
//
//   - bundleAutoEnable turns the unattended bundle attempt on. A test turns it
//     off: it runs against a temp home, and an attempt would invoke whichever
//     host CLI happens to be on the developer's PATH.
//   - projectAutoEnable turns the init-time project defaults on: the present
//     project files of a git checkout are enabled with secrets kept out. A test
//     turns it off so a suite running inside the beadle checkout does not
//     enable the repository's own project files in every temp vault.

func (a *app) resolveVault() (*vault.Vault, error) {
	root, err := vault.ResolveRoot(a.vaultPath, os.Getenv(vault.EnvHome))
	if err != nil {
		return nil, err
	}

	v := vault.New(root)
	if !vaultDirExists(root) {
		return nil, fmt.Errorf("vault %s: %w (run beadle init)", root, ErrVaultNotInitialized)
	}

	if err := a.ensureConfig(v); err != nil {
		return nil, err
	}

	return v, nil
}

func vaultDirExists(root string) bool {
	info, err := os.Stat(root) //nolint:gosec // G703: root is the resolved vault path, not request taint
	if err != nil || !info.IsDir() {
		return false
	}

	for _, marker := range []string{".gitignore", "state", "objects"} {
		if _, err := os.Stat(filepath.Join(root, marker)); err == nil { //nolint:gosec // G703: root is the resolved vault path, marker names are constants
			return true
		}
	}

	return false
}

func (a *app) ensureConfig(v *vault.Vault) error {
	if v.Initialized() {
		return nil
	}

	if err := config.Default().Save(v.ConfigPath()); err != nil {
		return err
	}

	// A one-time notice a user must see, on stderr: the split logger sends
	// errors there whatever the level is, and stdout stays a data channel.
	a.logger.Error(context.Background(), "config.json was missing; recreated with defaults")

	return nil
}

func (a *app) loadConfig() (*vault.Vault, *config.Config, error) {
	v, err := a.resolveVault()
	if err != nil {
		return nil, nil, err
	}

	cfg, err := config.Load(v.ConfigPath())
	if err != nil {
		return nil, nil, err
	}

	a.reportConfigMigration(cfg)

	return v, cfg, nil
}

// reportConfigMigration renders the one-time config v3 flips once per command
// on stderr, bypassing the log level: the migration itself stays in memory, so
// this line is the only trace a read-only command leaves.
func (a *app) reportConfigMigration(cfg *config.Config) {
	notes := cfg.MigrationNotes()
	if len(notes) == 0 {
		return
	}

	out := a.errOut
	if out == nil {
		out = os.Stderr
	}

	for _, note := range notes {
		fmt.Fprintln(out, "config: "+note)
	}

	cfg.TakeMigrationNotes()
}

// pluginClient returns the process-wide plugin manager client, opening it on
// first use. Every beadle command that needs the plugin manager goes through
// here, which is what makes "one client per process" true by construction
// rather than by convention.
func (a *app) pluginClient(v *vault.Vault, events chan<- verger.Event) (*vergerx.Client, error) {
	if a.plugins != nil || a.pluginsErr != nil {
		return a.plugins, a.pluginsErr
	}

	client, err := vergerx.Open(context.Background(), vergerx.Config{
		VaultRoot: v.Root(),
		Logger:    a.logger,
		Events:    events,
	})
	if err != nil {
		a.pluginsErr = err

		return nil, err
	}

	a.plugins = client

	return client, nil
}

func (a *app) engine(extra ...engine.Option) (*engine.Engine, error) {
	v, cfg, err := a.loadConfig()
	if err != nil {
		return nil, err
	}

	agents, err := allAgents()
	if err != nil {
		return nil, err
	}

	return a.engineWith(v, cfg, agents, extra...)
}

// engineWith builds the engine from an already loaded config: commands that
// load the config themselves must not load it twice, or the one-time
// migration notes would be rendered twice.
func (a *app) engineWith(v *vault.Vault, cfg *config.Config, agents []*agent.Agent, extra ...engine.Option) (*engine.Engine, error) {
	home, _, err := homeAndCwd()
	if err != nil {
		return nil, err
	}

	opts := []engine.Option{engine.WithLogger(a.logger), engine.WithHome(home)}
	if a.bundleAutoEnable {
		opts = append(opts, engine.WithBundleAutoEnable())
	}

	// The ownership invariant is wired here, once: with the plugin manager
	// open, every kind's pull asks it which paths it owns, so a plugin
	// artifact never enters the canon.
	//
	// A client that fails to open is not a vault without plugins — it is a
	// question beadle cannot answer. Continuing would leave the predicate
	// absent, and an absent predicate does not mean "nothing is owned": it
	// means every host file looks unowned, so the pull adopts what the plugin
	// manager wrote and the two tools start fighting over the same file. The
	// run stops instead, and `beadle doctor` names the verger home.
	var client *vergerx.Client

	if v.Initialized() {
		opened, err := a.pluginClient(v, nil)
		if err != nil {
			return nil, fmt.Errorf("open the plugin manager: %w", err)
		}

		client = opened
		opts = append(opts, engine.WithVergerOwns(client.Owns))
	}

	// The farm migration needs the manager itself, not only its ownership
	// predicate: it adopts, delivers and verifies.
	if a.managerOverride != nil {
		opts = append(opts, engine.WithPluginManager(a.managerOverride))
	} else if client != nil {
		opts = append(opts, engine.WithPluginManager(client))
	}

	return engine.New(v, cfg, agents, append(opts, extra...)...)
}

func homeAndCwd() (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", "", err
	}

	return home, cwd, nil
}

func allAgents() ([]*agent.Agent, error) {
	home, cwd, err := homeAndCwd()
	if err != nil {
		return nil, err
	}

	return agent.All(home, cwd), nil
}

func parseKinds(names []string) ([]kind.ID, error) {
	ids := make([]kind.ID, 0, len(names))

	for _, name := range names {
		id, err := kind.Parse(name)
		if err != nil {
			return nil, err
		}

		ids = append(ids, id)
	}

	return ids, nil
}
