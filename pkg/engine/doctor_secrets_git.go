package engine

import (
	"context"
	"fmt"

	"github.com/odiumuniverse/beadle/pkg/vault"
)

// A credential file that git has seen is a different problem from one it is
// tracking, and the difference decides who has to act.
//
// Out of the index but in a commit is the state that matters: the values are in
// the history, and they stay in the history whatever happens to the index now.
// Untracking a path does not unwrite a commit, and a file the user can no longer
// trust has to keep being reported after it leaves the index — otherwise the
// warning appears exactly once, at the moment it is least actionable, and the
// user acts on the index and believes the history is clean.
//
// beadle does not rewrite history. A rewrite rewrites every commit that touched
// the path, invalidates every clone, and needs a force-push somebody has to
// authorise with their own name; that is the user's git and the user's decision,
// and the finding carries the command instead of running it.
func (e *Engine) secretHistoryIssues(ctx context.Context) []Issue {
	var issues []Issue

	for _, path := range e.vault.CredentialPaths() {
		switch e.vault.CredentialExposure(ctx, path) {
		case vault.CredentialCommitted:
			issues = append(issues, Issue{
				Severity: SeverityWarning,
				Subject:  "vault.secrets-in-git-history",
				Message: fmt.Sprintf(
					"%s was tracked by git: its values may be in the history, and "+
						"untracking the file does not remove them from there. Rotate the "+
						"credentials, and rewrite the history if the repository is shared — "+
						"beadle does not rewrite history",
					path),
				// A finding with no command is a complaint. The command is the
				// user's own git, because the history is the user's.
				Fix: [][]string{{"git", "filter-repo", "--path", path, "--invert-paths"}},
			})
		case vault.CredentialStaged:
			// Not in the history yet, and this one beadle can fix: the sync
			// migration drops it from the index and leaves the file alone.
			issues = append(issues, Issue{
				Severity: SeverityWarning,
				Subject:  "vault.secrets-staged",
				Message: fmt.Sprintf(
					"%s is staged: the next commit would carry it. Run `beadle %s` to "+
						"drop it from the index — the file stays on this machine",
					path, syncCommand),
				Fix: [][]string{{beadleName, syncCommand}},
			})
		case vault.CredentialAbsent:
		}
	}

	return issues
}
