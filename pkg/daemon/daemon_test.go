package daemon_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/daemon"
)

func TestRenderLaunchd(t *testing.T) {
	t.Parallel()

	spec := daemon.Spec{
		Binary:     "/usr/local/bin/beadle",
		Args:       []string{"watch"},
		Home:       "/Users/test",
		LogPath:    "/Users/test/Library/Logs/agentsync.log",
		ErrLogPath: "/Users/test/Library/Logs/agentsync.err.log",
	}

	path, content, err := daemon.RenderLaunchd(spec)
	require.NoError(t, err)
	require.Equal(t, filepath.Join("/Users/test", "Library/LaunchAgents", daemon.DefaultLabel+".plist"), path)

	require.Contains(t, content, "<key>NumberOfFiles</key><integer>8192</integer>")
	require.Contains(t, content, "<string>/usr/local/bin/beadle</string>")
	require.Contains(t, content, "<string>watch</string>")
	require.Contains(t, content, "<key>KeepAlive</key><true/>")
	require.Contains(t, content, "<key>StandardOutPath</key><string>/Users/test/Library/Logs/agentsync.log</string>")
}

func TestRenderSystemd(t *testing.T) {
	t.Parallel()

	spec := daemon.Spec{
		Binary: "/usr/local/bin/beadle",
		Args:   []string{"watch"},
		Home:   "/home/test",
	}

	path, content, err := daemon.RenderSystemd(spec)
	require.NoError(t, err)
	require.Equal(t, filepath.Join("/home/test", ".config/systemd/user", "com-agentsync-watch.service"), path)

	require.Contains(t, content, "ExecStart=/usr/local/bin/beadle watch")
	require.Contains(t, content, "Restart=on-failure")
	require.Contains(t, content, "ProtectHome=no")
	require.Contains(t, content, "WantedBy=default.target")
}

func TestRenderRejectsRelativePaths(t *testing.T) {
	t.Parallel()

	_, _, err := daemon.RenderSystemd(daemon.Spec{Binary: "beadle", Home: "/home/test"})
	require.Error(t, err)

	_, _, err = daemon.RenderLaunchd(daemon.Spec{Binary: "/bin/beadle", Home: "relative"})
	require.Error(t, err)
}

func TestInstallWritesFileAndRegisters(t *testing.T) {
	t.Parallel()

	home := t.TempDir()

	var calls [][]string

	run := func(_ context.Context, name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))

		return nil
	}

	spec := daemon.Spec{Binary: "/bin/beadle", Args: []string{"watch"}, Home: home}

	path, err := daemon.Install(t.Context(), spec, run)
	require.NoError(t, err)

	_, err = os.Stat(path)
	require.NoError(t, err)
	require.NotEmpty(t, calls)
}
