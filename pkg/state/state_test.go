package state_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/cas"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/state"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), state.FileName)

	st := state.New()
	st.SetBase(kind.MCP, "claude-code", state.Base{"alpha": cas.HashOf([]byte("a"))})
	st.ReplaceConflicts(kind.MCP, "opencode", []state.Conflict{{
		Kind: kind.MCP, Agent: "opencode", Key: "alpha", Reason: state.ReasonModified, Since: time.Unix(100, 0).UTC(),
	}})

	require.NoError(t, st.Save(path))

	loaded, err := state.Load(path)
	require.NoError(t, err)

	base, ok := loaded.Base(kind.MCP, "claude-code")
	require.True(t, ok)
	require.Equal(t, cas.HashOf([]byte("a")), base["alpha"])

	_, ok = loaded.Base(kind.MCP, "opencode")
	require.False(t, ok, "an agent that never synchronized has no base")

	require.Len(t, loaded.OpenConflicts(), 1)
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	t.Parallel()

	st, err := state.Load(filepath.Join(t.TempDir(), "missing.json"))
	require.NoError(t, err)
	require.Empty(t, st.OpenConflicts())
}

func TestLoadRejectsOtherVersions(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), state.FileName)
	require.NoError(t, os.WriteFile(path, []byte(`{"version": 1}`), 0o600))

	_, err := state.Load(path)
	require.Error(t, err)
}

func TestLoadReinitializesNullMaps(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), state.FileName)
	require.NoError(t, os.WriteFile(path, []byte(`{"version": 2, "bases": null, "snapshots": null, "renders": null, "drift": null}`), 0o600))

	st, err := state.Load(path)
	require.NoError(t, err)

	st.Renders["/repo/AGENTS.md"] = state.Render{Notes: 1}
	st.Drift["/repo/AGENTS.md"] = state.Drift{Count: 1}
	st.SetBase(kind.Rules, "a", nil)

	require.NoError(t, st.Save(path))

	again, err := state.Load(path)
	require.NoError(t, err)
	require.Contains(t, again.Renders, "/repo/AGENTS.md")
	require.Contains(t, again.Drift, "/repo/AGENTS.md")
}

func TestReplaceConflictsKeepsDetectionTime(t *testing.T) {
	t.Parallel()

	st := state.New()
	first := state.Conflict{Kind: kind.Rules, Agent: "a", Key: kind.RulesKey, Since: time.Unix(100, 0).UTC()}
	st.ReplaceConflicts(kind.Rules, "a", []state.Conflict{first})

	again := first
	again.Since = time.Unix(200, 0).UTC()
	st.ReplaceConflicts(kind.Rules, "a", []state.Conflict{again})

	require.Equal(t, time.Unix(100, 0).UTC(), st.OpenConflicts()[0].Since)

	other := state.Conflict{Kind: kind.Rules, Agent: "b", Key: kind.RulesKey}
	st.ReplaceConflicts(kind.Rules, "b", []state.Conflict{other})
	st.ReplaceConflicts(kind.Rules, "a", nil)

	require.Equal(t, []state.Conflict{other}, st.OpenConflicts(), "replacing one agent never touches another")
}

func TestConflictLookup(t *testing.T) {
	t.Parallel()

	st := state.New()
	c := state.Conflict{Kind: kind.MCP, Agent: "cursor", Key: "alpha"}
	st.ReplaceConflicts(kind.MCP, "cursor", []state.Conflict{c})

	found, err := st.Conflict(c.ID()[:4])
	require.NoError(t, err)
	require.Equal(t, c.ID(), found.ID())

	_, err = st.Conflict("zzzz")
	require.Error(t, err)

	st.RemoveConflict(c.ID())
	require.Empty(t, st.OpenConflicts())
}

func TestSnapshotsAreBoundedAndDeduplicated(t *testing.T) {
	t.Parallel()

	st := state.New()

	for i := range state.MaxSnapshots + 5 {
		st.AddSnapshot(kind.Rules, state.Snapshot{Manifest: cas.HashOf(fmt.Appendf(nil, "%d", i))})
	}

	history := st.History(kind.Rules)
	require.Len(t, history, state.MaxSnapshots)

	st.AddSnapshot(kind.Rules, history[len(history)-1])
	require.Len(t, st.History(kind.Rules), state.MaxSnapshots, "an unchanged vault adds no snapshot")
}

func TestHashesCoverEveryReference(t *testing.T) {
	t.Parallel()

	st := state.New()
	st.SetBase(kind.Skills, "claude-code", state.Base{"a/SKILL.md": "h1"})
	st.ReplaceConflicts(kind.Skills, "claude-code", []state.Conflict{{Kind: kind.Skills, Agent: "claude-code", Key: "a/SKILL.md", Local: "h2"}})
	st.AddSnapshot(kind.Skills, state.Snapshot{Manifest: "h3"})

	require.Equal(t, []cas.Hash{"h1", "h2", "h3"}, st.Hashes())
}
