package engine

import (
	"fmt"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/daemon"
)

// daemonEnvIssues compares the environment pinned in the service unit with the
// current home and vault: a watcher installed for another tree syncs the wrong
// vault, and a unit without pinned values predates the pinning.
func (e *Engine) daemonEnvIssues(status daemon.Status) []Issue {
	if status.Label != daemon.DefaultLabel {
		// A Homebrew-managed unit is not beadle's to reinstall.
		return nil
	}

	unit, err := daemon.UnitEnvFromFile(status.Path)
	if err != nil {
		return []Issue{{Severity: SeverityWarn, Message: "cannot read the daemon unit environment: " + err.Error()}}
	}

	if len(unit) == 0 {
		return []Issue{{Severity: SeverityWarn, Message: "daemon unit pins no environment (installed before env pinning); reinstall it with `beadle daemon install` so the watcher follows this vault"}}
	}

	expected := daemon.UnitEnv(e.home, e.vault.Root())

	var mismatched []string

	for _, key := range daemon.IdentityEnvKeys {
		if unit[key] != expected[key] {
			mismatched = append(mismatched, fmt.Sprintf("%s is %q (now %q)", key, unit[key], expected[key]))
		}
	}

	if len(mismatched) == 0 {
		return nil
	}

	return []Issue{{Severity: SeverityWarn, Message: fmt.Sprintf(
		"daemon unit environment does not match this vault: %s; reinstall it with `beadle daemon install`",
		strings.Join(mismatched, ", "))}}
}
