package engine

import (
	"context"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/skill"
)

// piMCPAdapterIssues warns that servers beadle manages on the Pi MCP surface
// only work with the third-party pi-mcp-adapter: Pi itself has no MCP
// support. It stays silent when no managed server reaches the surface, when
// the MCP kind is off for Pi, or when the adapter is detected.
func (e *Engine) piMCPAdapterIssues(ctx context.Context, active []*agent.Agent) []Issue {
	if e.home == "" {
		return nil
	}

	pi := agent.ByID(active, agent.PiID)
	if pi == nil || !e.config.KindEnabled(kind.MCP) {
		return nil
	}

	surface := pi.Surface(kind.MCP)
	if surface == nil || e.config.ModeFor(agent.PiID, kind.MCP, surface.Traits().DefaultMode) == config.ModeOff {
		return nil
	}

	servers := e.managedServersOn(ctx, surface)
	if len(servers) == 0 || agent.PiMCPAdapterPresent(e.home, e.cwd) {
		return nil
	}

	return []Issue{{
		Severity: SeverityWarn, Kind: kind.MCP, Agent: agent.PiID,
		Message: fmt.Sprintf(
			"Pi has no built-in MCP: the managed server(s) %s in %s are read only by the third-party pi-mcp-adapter (github.com/nicobailon/pi-mcp-adapter), which beadle did not detect; run pi install npm:pi-mcp-adapter or they stay inert",
			strings.Join(servers, ", "), displayHomePath(surface.Path(), e.home)),
	}}
}

// managedServersOn lists the canon MCP server names the surface holds, so a
// finding names exactly the servers beadle manages there.
func (e *Engine) managedServersOn(ctx context.Context, surface agent.Surface) []string {
	snap, err := surface.Read(ctx)
	if err != nil {
		return nil
	}

	canon, _, err := e.loadVault(kind.MCP)
	if err != nil {
		return nil
	}

	found := map[string]struct{}{}

	for name := range snap.Items {
		if _, managed := canon[name]; managed {
			found[name] = struct{}{}
		}
	}

	return slices.Sorted(maps.Keys(found))
}

// kiloLegacySkillIssues reports the skill copies Kilo keeps outside the
// canonical write target. The host code reads {configDir}/{skill,skills} and
// the docs name ~/.kilo/skills; beadle reads all of them and writes only
// ~/.config/kilo/skills, so the other copies are surfaced here instead of
// being silently duplicated: an Info for foreign copies, a Warn when a copy
// already duplicates the canon and manual cleanup is due.
func (e *Engine) kiloLegacySkillIssues(active []*agent.Agent) []Issue {
	if e.home == "" {
		return nil
	}

	kilo := agent.ByID(active, agent.KiloID)
	if kilo == nil || !e.config.KindEnabled(kind.Skills) {
		return nil
	}

	surface := kilo.Surface(kind.Skills)
	if surface == nil {
		return nil
	}

	items, _, err := e.loadVault(kind.Skills)
	if err != nil {
		return nil
	}

	canon := skill.Group(items)

	var (
		legacy int
		dupes  int
		dirs   []string
	)

	for _, dir := range agent.KiloSkillReadDirs(e.home) {
		if dir == surface.Path() || !isDir(dir) {
			continue
		}

		names, err := skillNamesIn(dir)
		if err != nil || len(names) == 0 {
			continue
		}

		dirs = append(dirs, displayHomePath(dir, e.home))
		legacy += len(names)
		dupes += sameSkillTrees(dir, names, canon)
	}

	if legacy == 0 {
		return nil
	}

	location := strings.Join(dirs, ", ")
	target := displayHomePath(surface.Path(), e.home)
	mode := e.config.ModeFor(agent.KiloID, kind.Skills, surface.Traits().DefaultMode)

	if dupes > 0 {
		return []Issue{{
			Severity: SeverityWarn, Kind: kind.Skills, Agent: agent.KiloID,
			Message: fmt.Sprintf("Kilo: %d skill(s) in %s duplicate the canon; %s", dupes, location, kiloRemovalHint(surface, mode, target)),
		}}
	}

	return []Issue{{
		Severity: SeverityInfo, Kind: kind.Skills, Agent: agent.KiloID,
		Message: fmt.Sprintf(
			"Kilo: %d skill(s) live in %s, outside the canonical %s the host code reads; beadle reads both and never writes or deletes there; %s",
			legacy, location, target, kiloMoveHint(mode, target)),
	}}
}

// kiloRemovalHint words the action for copies whose skill is already in the
// canon (the Warn branch): beadle can restore those, so removal is safe, but
// only when beadle itself writes the canonical directory.
func kiloRemovalHint(surface agent.Surface, mode config.Mode, target string) string {
	switch {
	case mode.Pushes() && isDir(surface.Path()):
		return fmt.Sprintf("beadle writes %s, so remove the copies manually to keep one location", target)
	case mode.Pushes():
		return fmt.Sprintf(
			"beadle writes %s only once the directory exists (skills surfaces are not creatable): create it and run beadle sync, then remove the copies manually", target)
	default:
		return kiloMoveHint(mode, target)
	}
}

// kiloMoveHint words the action for copies beadle cannot restore: the Info
// branch (the skill is not in the canon) always moves them, never removes
// them, because the copy is the only delivery. Moving to the canonical
// directory also hands the skill to beadle: a pulling mode reads it into the
// canon on the next sync, a push-only mode needs the mode switch first.
func kiloMoveHint(mode config.Mode, target string) string {
	if mode.Pulls() {
		return "move them to " + target + "; beadle pulls the canonical directory into the canon on the next sync"
	}

	return "run beadle agents mode kilo skills sync, then move them to " + target + " and run beadle sync"
}

// skillNamesIn lists the valid skill directories directly under dir, the
// same root rule the skills surfaces use (a directory with a root SKILL.md).
// Symlinked skill directories count: the host follows them too.
func skillNamesIn(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var names []string

	for _, entry := range entries {
		name := entry.Name()
		if !skill.ValidName(name) {
			continue
		}

		path := filepath.Join(dir, name)

		if entry.Type()&fs.ModeSymlink != 0 {
			info, err := os.Stat(path)
			if err != nil || !info.IsDir() {
				continue
			}
		} else if !entry.IsDir() {
			continue
		}

		if skill.HasRoot(path) {
			names = append(names, name)
		}
	}

	return names, nil
}

// sameSkillTrees counts the named skills whose tree matches the canon.
func sameSkillTrees(dir string, names []string, canon map[string]skill.Tree) int {
	count := 0

	for _, name := range names {
		tree, ok := canon[name]
		if !ok {
			continue
		}

		read, err := skill.ReadTree(filepath.Join(dir, name))
		if err != nil {
			continue
		}

		if skill.TreeDigest(read) == skill.TreeDigest(tree) {
			count++
		}
	}

	return count
}
