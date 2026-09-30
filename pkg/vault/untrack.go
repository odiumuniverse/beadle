package vault

import (
	"context"
	"os/exec"
	"strings"
)

// UntrackVergerState stops git from tracking the files under the library's home
// that record what one machine did: receipts, the journal, tombstones, consent,
// secrets, trust, the watch lease, the store. They stay on the machine and stay
// on disk — the vault travels in git without them, and the next machine
// rebuilds them from the spec and the lock, which do travel.
//
// This is a migration, not a policy: a vault committed before these rules
// already tracks them, and `.gitignore` alone does not untrack a path. It runs
// once and is idempotent — a second run finds nothing tracked and says nothing.
//
// It returns the paths it untracked, so the caller can tell the user; git is
// the user's tool, so nothing is committed and nothing is deleted.
func (v *Vault) UntrackVergerState(ctx context.Context) ([]string, error) {
	if !v.inGitWorkTree(ctx) {
		return nil, nil
	}

	tracked := v.trackedVergerState(ctx)

	if len(tracked) == 0 {
		return nil, nil
	}

	args := append([]string{"rm", "--cached", "--quiet", "--"}, tracked...)
	if out, err := runGit(ctx, v.Root(), args...); err != nil {
		return nil, err
	} else if out != "" {
		// git's own chatter is not an error, but a line naming a path it could
		// not remove is worth surfacing rather than swallowing.
		if strings.Contains(out, "error") {
			return nil, untrackError{Output: out}
		}
	}

	return tracked, nil
}

// trackedVergerState lists the machine-local paths git currently tracks.
func (v *Vault) trackedVergerState(ctx context.Context) []string {
	out, err := runGit(ctx, v.Root(), "ls-files", "-z", "--")
	if err != nil {
		return nil
	}

	tracked := map[string]bool{}

	for name := range strings.SplitSeq(out, "\x00") {
		if name != "" {
			tracked[name] = true
		}
	}

	var found []string

	for _, path := range vergerStateFiles {
		if tracked[path] {
			found = append(found, path)
			continue
		}

		// A directory rule covers the files inside it, so a tracked file under
		// one is the real case.
		prefix := strings.TrimSuffix(path, "/") + "/"

		for name := range tracked {
			if strings.HasPrefix(name, prefix) {
				found = append(found, name)
			}
		}
	}

	return found
}

func (v *Vault) inGitWorkTree(ctx context.Context) bool {
	_, err := runGit(ctx, v.Root(), "rev-parse", "--is-inside-work-tree")

	return err == nil
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // G204: git is invoked with the vault's own paths on purpose
	cmd.Dir = dir

	out, err := cmd.Output()

	return string(out), err
}

// untrackError is a git refusal to stop tracking something, with git's own
// words.
type untrackError struct{ Output string }

func (e untrackError) Error() string {
	return "git refused to stop tracking the machine-local files: " + e.Output
}
