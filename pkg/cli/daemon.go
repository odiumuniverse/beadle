package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/beadle/pkg/daemon"
)

var daemonInstallRunner daemon.Runner = execRunner

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
			spec, err := a.daemonSpec()
			if err != nil {
				return err
			}

			path, err := daemon.Install(cmd.Context(), spec, daemonInstallRunner)
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
			spec, err := a.daemonSpec()
			if err != nil {
				return err
			}

			path, err := daemon.Uninstall(cmd.Context(), spec, daemonInstallRunner)
			if err != nil {
				return err
			}

			cmd.Printf("service removed: %s\n", path)

			return nil
		},
	}
}

func (a *app) daemonSpec() (daemon.Spec, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return daemon.Spec{}, err
	}

	binary, err := os.Executable()
	if err != nil {
		return daemon.Spec{}, err
	}

	if resolved, err := filepath.EvalSymlinks(binary); err == nil {
		binary = resolved
	}

	v, err := a.resolveVault()
	if err != nil {
		return daemon.Spec{}, err
	}

	spec := daemon.Spec{Binary: binary, Args: []string{"watch", "--vault", v.Root()}, Home: home}

	if runtime.GOOS == "darwin" {
		spec.LogPath = filepath.Join(home, "Library", "Logs", "beadle.log")
		spec.ErrLogPath = filepath.Join(home, "Library", "Logs", "beadle.err.log")
	}

	return spec, nil
}

func execRunner(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: only launchctl/systemctl are executed, with fixed arguments
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}
