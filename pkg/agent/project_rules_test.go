package agent_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/digest"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/memory"
)

type fencedWriter interface {
	WriteFenced(context.Context, []byte) error
}

func projectSurface(t *testing.T, home, cwd string) agent.Surface {
	t.Helper()

	return surfaceOf(t, agent.OpenCode(home, cwd), kind.Projects)
}

func projectKey(cwd string) string {
	return memory.Slug(cwd) + "/AGENTS.md"
}

func blockFor(t *testing.T, slug string) []byte {
	t.Helper()

	notes := map[string][]byte{slug + "/MEMORY.md": []byte("---\ndescription: hook\n---\nbody\n")}
	block, _ := digest.Render("/home/u/.claude/projects/"+slug+"/memory", notes, digest.DefaultBudget)

	return block
}

func TestProjectSurfaceInactive(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cwd := t.TempDir()
	surface := projectSurface(t, home, cwd)

	snap, err := surface.Read(t.Context())
	require.NoError(t, err)
	require.False(t, snap.Present)
	require.Empty(t, snap.Items)

	require.NoError(t, surface.Write(t.Context(), kind.Items{projectKey(cwd): []byte("body\n")}))
	require.NoFileExists(t, filepath.Join(cwd, "AGENTS.md"))

	projector, ok := surface.(agent.Projector)
	require.True(t, ok)

	_, _, visible := projector.Project(projectKey(cwd), []byte("body\n"))
	require.False(t, visible)
}

func TestProjectSurfaceActiveWithoutFile(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cwd := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(cwd, ".git"), 0o750))

	surface := projectSurface(t, home, cwd)

	snap, err := surface.Read(t.Context())
	require.NoError(t, err)
	require.True(t, snap.Present)
	require.Empty(t, snap.Items)

	require.NoError(t, surface.Write(t.Context(), kind.Items{projectKey(cwd): []byte("# rules\n")}))
	require.Equal(t, "# rules\n", readFile(t, filepath.Join(cwd, "AGENTS.md")))
}

func TestProjectSurfaceReadIsFenceBlind(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cwd := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(cwd, ".git"), 0o750))

	surface := projectSurface(t, home, cwd)
	block := blockFor(t, memory.Slug(cwd))

	writeFile(t, filepath.Join(cwd, "AGENTS.md"), string(block)+"# user rules\n\nText.\n")

	snap, err := surface.Read(t.Context())
	require.NoError(t, err)
	require.True(t, snap.Present)
	require.Equal(t, "# user rules\n\nText.\n", string(snap.Items[projectKey(cwd)]), "the fence never reaches items")

	writeFile(t, filepath.Join(cwd, "AGENTS.md"), string(block))

	snap, err = surface.Read(t.Context())
	require.NoError(t, err)
	require.Empty(t, snap.Items, "a fence-only file has no item")

	writeFile(t, filepath.Join(cwd, "AGENTS.md"), string(block)+"x\n"+digest.BeginPrefix+" broken\n"+digest.EndMarker+"\n")

	_, err = surface.Read(t.Context())
	require.ErrorIs(t, err, digest.ErrFence)
}

func TestProjectSurfaceWriteKeepsFence(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cwd := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(cwd, ".git"), 0o750))

	surface := projectSurface(t, home, cwd)
	block := blockFor(t, memory.Slug(cwd))
	path := filepath.Join(cwd, "AGENTS.md")

	writeFile(t, path, string(block)+"# v1\n")
	require.NoError(t, os.Chmod(path, 0o600))

	require.NoError(t, surface.Write(t.Context(), kind.Items{projectKey(cwd): []byte("# v2\n")}))

	body, fence, found, err := digest.Strip([]byte(readFile(t, path)))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "# v2\n", string(body))
	require.Equal(t, string(block), string(fence))

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the mode of an existing file survives")
}

func TestProjectSurfaceWriteFenced(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cwd := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(cwd, ".git"), 0o750))

	surface := projectSurface(t, home, cwd)
	writer, ok := surface.(fencedWriter)
	require.True(t, ok)

	path := filepath.Join(cwd, "AGENTS.md")
	block := blockFor(t, memory.Slug(cwd))

	require.NoError(t, writer.WriteFenced(t.Context(), block))
	body, fence, found, err := digest.Strip([]byte(readFile(t, path)))
	require.NoError(t, err)
	require.True(t, found)
	require.Empty(t, body)
	require.Equal(t, string(block), string(fence))

	writeFile(t, path, string(block)+"# local body\n")

	fresh := blockFor(t, memory.Slug(cwd))
	require.NoError(t, writer.WriteFenced(t.Context(), fresh))

	body, fence, found, err = digest.Strip([]byte(readFile(t, path)))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "# local body\n", string(body), "WriteFenced never touches the body")
	require.Equal(t, string(fresh), string(fence))

	require.NoError(t, writer.WriteFenced(t.Context(), nil))

	require.Equal(t, "# local body\n", readFile(t, path))
}

func TestProjectSurfaceNormalizesUnterminatedFence(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cwd := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(cwd, ".git"), 0o750))

	surface := projectSurface(t, home, cwd)
	path := filepath.Join(cwd, "AGENTS.md")
	block := blockFor(t, memory.Slug(cwd))

	writeFile(t, path, string(bytes.TrimSuffix(block, []byte("\n"))))

	require.NoError(t, surface.Write(t.Context(), kind.Items{projectKey(cwd): []byte("\n# body\n")}))

	body, fence, found, err := digest.Strip([]byte(readFile(t, path)))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "\n# body\n", string(body), "a leading newline of the body survives")
	require.True(t, strings.HasSuffix(string(fence), "\n"))
}

func TestProjectSurfaceWriteDeletesCanonicalFile(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cwd := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(cwd, ".git"), 0o750))

	surface := projectSurface(t, home, cwd)
	path := filepath.Join(cwd, "AGENTS.md")
	block := blockFor(t, memory.Slug(cwd))

	writeFile(t, path, string(block)+"# body\n")
	require.NoError(t, surface.Write(t.Context(), kind.Items{}))
	require.NoFileExists(t, path, "a file that carried canonical content is removed")

	writeFile(t, path, string(block))
	require.NoError(t, surface.Write(t.Context(), kind.Items{}))
	require.FileExists(t, path, "a fence-only file is never deleted")

	writeFile(t, path, "# body\n")

	link := "sibling.md"
	writeFile(t, filepath.Join(cwd, link), "# else\n")
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Symlink(filepath.Join(cwd, link), path))
	require.NoError(t, surface.Write(t.Context(), kind.Items{}))
	require.FileExists(t, filepath.Join(cwd, link), "a symlink is not deleted")
}

func TestProjectSurfaceProject(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cwd := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(cwd, ".git"), 0o750))

	surface := projectSurface(t, home, cwd)
	projector, ok := surface.(agent.Projector)
	require.True(t, ok)

	key, value, visible := projector.Project(projectKey(cwd), []byte("# rules\n"))
	require.True(t, visible)
	require.Equal(t, projectKey(cwd), key)
	require.Equal(t, "# rules\n", string(value))

	_, _, visible = projector.Project(memory.Slug(t.TempDir())+"/AGENTS.md", []byte("x"))
	require.False(t, visible, "another project slug is hidden")

	_, _, visible = projector.Project(memory.Slug(cwd)+"/GEMINI.md", []byte("x"))
	require.False(t, visible, "another file of the same project is hidden")
}
