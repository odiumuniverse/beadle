package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/vmkteam/embedlog"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/vault"
)

type app struct {
	logger    embedlog.Logger
	vaultPath string
	// errOut is the command's stderr, set on every run. Migration notes go
	// here as plain lines: the logger may be level-filtered (--log-json
	// without --verbose drops info), while a one-time default flip must
	// always be visible, and stdout stays machine-readable.
	errOut io.Writer
}

// bundleAutoEnable turns the unattended bundle attempt on for every CLI
// engine. Tests flip it off: they run against temp homes, and an attempt
// would invoke whichever host CLI happens to be on the developer's PATH.
var bundleAutoEnable = true

// projectAutoEnable turns the init-time project defaults on: the present
// project files of a git checkout are enabled with secrets kept out. Tests
// flip it off so a suite running inside the beadle checkout does not enable
// the repository's own project files in every temp vault.
var projectAutoEnable = true

func (a *app) resolveVault() (*vault.Vault, error) {
	root, err := vault.ResolveRoot(a.vaultPath, os.Getenv(vault.EnvHome))
	if err != nil {
		return nil, err
	}

	v := vault.New(root)
	if !vaultDirExists(root) {
		return nil, fmt.Errorf("vault %s is not initialized (run beadle init)", root)
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

	a.logger.Print(context.Background(), "config.json was missing; recreated with defaults", "vault", v.Root())

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
	if bundleAutoEnable {
		opts = append(opts, engine.WithBundleAutoEnable())
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
