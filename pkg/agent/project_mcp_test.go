package agent_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
	proj "github.com/odiumuniverse/beadle/pkg/project"
)

func projectMCPSurfaceOf(t *testing.T, cwd string) (agent.Surface, string) {
	t.Helper()

	surface := agent.ClaudeCode(t.TempDir(), cwd).Surface(kind.Projects)
	require.NotNil(t, surface)

	file, ok := surface.(agent.ProjectFile)
	require.True(t, ok)

	return surface, proj.Resolve(cwd).ID + "/" + file.ProjectRel()
}

func TestProjectMCPSurfaceWriteRefusesLinks(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cwd := t.TempDir()

	surface, key := projectMCPSurfaceOf(t, cwd)

	target := filepath.Join(home, "target.json")
	writeFile(t, target, `{"orig": true}`)

	require.NoError(t, os.Symlink(target, filepath.Join(cwd, ".mcp.json")))

	err := surface.Write(t.Context(), kind.Items{key: []byte(`{"mcpServers": {"evil": {}}}`)})
	require.ErrorContains(t, err, "symlink")
	require.JSONEq(t, `{"orig": true}`, readFile(t, target), "no write-through into the symlink target")

	info, err := os.Lstat(filepath.Join(cwd, ".mcp.json"))
	require.NoError(t, err)
	require.NotEqual(t, os.FileMode(0), info.Mode()&os.ModeSymlink, "the symlink stays untouched")

	require.NoError(t, os.Remove(filepath.Join(cwd, ".mcp.json")))

	other := filepath.Join(cwd, "shared.json")
	writeFile(t, other, `{"mcpServers": {}}`)
	require.NoError(t, os.Link(other, filepath.Join(cwd, ".mcp.json")))

	err = surface.Write(t.Context(), kind.Items{key: []byte(`{"mcpServers": {"evil": {}}}`)})
	require.ErrorContains(t, err, "hard links")
	require.JSONEq(t, `{"mcpServers": {}}`, readFile(t, other), "the hard-link sibling is untouched")
}

func TestProjectRulesSurfaceRefusesSymlinkRead(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cwd := t.TempDir()

	secret := filepath.Join(home, "id_rsa")
	writeFile(t, secret, "PRIVATE KEY MATERIAL\n")
	require.NoError(t, os.Symlink(secret, filepath.Join(cwd, "AGENTS.md")))

	a := agent.OpenCode(home, cwd)
	surface := a.Surface(kind.Projects)
	require.NotNil(t, surface)

	_, err := surface.Read(t.Context())
	require.ErrorContains(t, err, "symlink", "a symlinked rules file is refused on read")
}
