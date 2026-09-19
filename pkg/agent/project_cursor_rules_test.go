package agent_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

func cursorRulesSurfaceOf(t *testing.T, cwd string) agent.Surface {
	t.Helper()

	for _, surface := range agent.Cursor(t.TempDir(), cwd).SurfacesOf(kind.Projects) {
		if file, ok := surface.(agent.ProjectFile); ok && file.ProjectRel() == ".cursor/rules" {
			return surface
		}
	}

	t.Fatal("no .cursor/rules surface")

	return nil
}

func TestCursorRulesSurfaceReadsOnlyValidNames(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	dir := filepath.Join(cwd, ".cursor", "rules")

	writeFile(t, filepath.Join(dir, "style.mdc"), "# style\n")
	writeFile(t, filepath.Join(dir, "A-B_c.mdc"), "# mixed\n")
	writeFile(t, filepath.Join(dir, ".hidden.mdc"), "# hidden\n")
	writeFile(t, filepath.Join(dir, "notes.txt"), "# not mdc\n")
	writeFile(t, filepath.Join(dir, "sub", "nested.mdc"), "# nested\n")

	surface := cursorRulesSurfaceOf(t, cwd)

	snap, err := surface.Read(t.Context())
	require.NoError(t, err)
	require.Len(t, snap.Items, 2, "only flat [A-Za-z0-9][A-Za-z0-9._-]*.mdc names are rules")

	for key := range snap.Items {
		require.Regexp(t, `^[^/]+/\.cursor/rules/[A-Za-z0-9][A-Za-z0-9._-]*\.mdc$`, key)
	}
}

func TestCursorRulesSurfaceWriteKeepsForeignFiles(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	dir := filepath.Join(cwd, ".cursor", "rules")

	writeFile(t, filepath.Join(dir, "style.mdc"), "# style\n")
	writeFile(t, filepath.Join(dir, "old.mdc"), "# old\n")
	writeFile(t, filepath.Join(dir, "notes.txt"), "foreign\n")

	surface := cursorRulesSurfaceOf(t, cwd)

	snap, err := surface.Read(t.Context())
	require.NoError(t, err)
	require.Len(t, snap.Items, 2)

	styleKey := ""

	for key := range snap.Items {
		if filepath.Base(key) == "style.mdc" {
			styleKey = key
		}
	}

	require.NotEmpty(t, styleKey)

	prefix := styleKey[:len(styleKey)-len("style.mdc")]

	desired := kind.Items{
		styleKey:                 []byte("# style v2\n"),
		prefix + "new.mdc":       []byte("# new\n"),
		prefix + "ghost.mdc":     []byte("# ghost\n"),
		prefix + "notes.txt":     []byte("# ignored\n"),
		prefix + "sub/nested.md": []byte("# ignored\n"),
	}

	require.NoError(t, surface.Write(t.Context(), desired))

	require.Equal(t, "# style v2\n", readFile(t, filepath.Join(dir, "style.mdc")))
	require.Equal(t, "# new\n", readFile(t, filepath.Join(dir, "new.mdc")))
	require.Equal(t, "# ghost\n", readFile(t, filepath.Join(dir, "ghost.mdc")), "a valid new name is created")
	require.NoFileExists(t, filepath.Join(dir, "old.mdc"), "a rule missing from desired is removed")
	require.NoFileExists(t, filepath.Join(dir, "sub", "nested.mdc"), "invalid keys are never written")
	require.Equal(t, "foreign\n", readFile(t, filepath.Join(dir, "notes.txt")), "foreign files stay")
}
