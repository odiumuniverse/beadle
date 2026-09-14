package sync

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/odiumuniverse/agents-sync/pkg/config"
	"github.com/odiumuniverse/agents-sync/pkg/skill"
)

func (e *Engine) pruneAgentSkills(agentID string, state *canon) error {
	a := e.adapterByID(agentID)
	if a == nil || !a.SkillsPush() || e.skillMode(agentID) != config.SkillsSync {
		return nil
	}

	dir := a.SkillsDir()
	if dir == "" {
		return nil
	}

	current, err := skill.ReadDir(dir)
	if err != nil {
		return err
	}

	for name := range current {
		if _, ok := state.skills[name]; ok {
			continue
		}

		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			return fmt.Errorf("prune skill %s: %w", name, err)
		}
	}

	return nil
}
