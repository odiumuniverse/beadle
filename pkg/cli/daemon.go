package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/beadle/pkg/daemon"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/secret"
)

// temporaryHome is the temporary-root check the installer consults. It is a
// field on the app (Options.DaemonTemporaryHome), not a package variable, so a
// test that lifts this check does it for its own run.
func (a *app) temporaryHome(home string) bool {
	if a.daemonTemporaryHome != nil {
		return a.daemonTemporaryHome(home)
	}

	return daemon.TemporaryHome(home)
}

// not been given one. It is a default, not the source of truth: a.daemonInstall
// is what every call site reads, so a test can hand the app a recorder without
// touching anything another test is doing.
func (a *app) installRunner() daemon.Runner {
	if a.daemonInstall != nil {
		return a.daemonInstall
	}

	return execRunner
}

// checkRunner is the process call the init-time daemon probe makes, with the
// same contract as installRunner.
func (a *app) checkRunner() secret.Runner {
	if a.daemonCheck != nil {
		return a.daemonCheck
	}

	return secret.ExecRunner{}
}

func (a *app) newDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the background watcher service",
	}

	cmd.AddCommand(a.newDaemonInstallCmd(), a.newDaemonUninstallCmd())

	return cmd
}

func (a *app) newDaemonInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install and start the watcher as a background service",
		RunE: func(cmd *cobra.Command, _ []string) error {
			spec, vaultRoot, err := a.daemonSpec()
			if err != nil {
				return err
			}

			if err := a.temporaryDaemonRefusal(spec, vaultRoot, "use a permanent HOME/BEADLE_HOME"); err != nil {
				return err
			}

			path, err := daemon.Install(cmd.Context(), spec, a.installRunner())
			if err != nil {
				return err
			}

			cmd.Printf("service installed: %s\n", path)

			if runtime.GOOS == "linux" {
				cmd.Println("hint: run `loginctl enable-linger $USER` so the service survives logout")
			}

			return nil
		},
	}
}

func (a *app) newDaemonUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Stop and remove the background service",
		RunE: func(cmd *cobra.Command, _ []string) error {
			spec, _, err := a.daemonSpec()
			if err != nil {
				return err
			}

			path, err := daemon.Uninstall(cmd.Context(), spec, a.installRunner())
			if err != nil {
				return err
			}

			cmd.Printf("service removed: %s\n", path)

			return nil
		},
	}
}

// temporaryDaemonRefusal refuses a daemon install for a temporary home or
// vault: the unit would outlive the temporary tree, and without a pinned
// environment the watcher could sync a different home. The hint names the way
// out of the calling command.
func (a *app) temporaryDaemonRefusal(spec daemon.Spec, vaultRoot, hint string) error {
	var temporary []string

	for _, path := range []string{spec.Home, vaultRoot} {
		if a.temporaryHome(path) {
			temporary = append(temporary, path)
		}
	}

	if len(temporary) == 0 {
		return nil
	}

	return fmt.Errorf(
		"refusing to install the background watcher for a temporary path (%s): the service would outlive the temporary tree; %s",
		strings.Join(temporary, ", "), hint)
}

// daemonSpec returns the service spec and the vault root it watches.
func (a *app) daemonSpec() (daemon.Spec, string, error) {
	home, err := fsutil.UserHome()
	if err != nil {
		return daemon.Spec{}, "", err
	}

	binary, err := os.Executable()
	if err != nil {
		return daemon.Spec{}, "", err
	}

	if resolved, err := filepath.EvalSymlinks(binary); err == nil {
		binary = resolved
	}

	v, err := a.resolveVault()
	if err != nil {
		return daemon.Spec{}, "", err
	}

	spec := daemon.Spec{
		Binary: binary,
		Args:   []string{"watch", "--vault", v.Root()},
		Home:   home,
		Env:    daemon.UnitEnv(home, v.Root()),
	}

	if runtime.GOOS == "darwin" {
		spec.LogPath = filepath.Join(home, "Library", "Logs", "beadle.log")
		spec.ErrLogPath = filepath.Join(home, "Library", "Logs", "beadle.err.log")
	}

	return spec, v.Root(), nil
}

func execRunner(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: only launchctl/systemctl are executed, with fixed arguments
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}
