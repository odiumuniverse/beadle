package vault

import (
	"context"
	"os/exec"
	"slices"
	"strings"
)

// UntrackMachineLocal stops git from tracking what one machine produced: the
// library's receipts, journal, tombstones, consent, secrets, trust, the watch
// lease and the store, and the vault's own machine-local trees — state/,
// objects/, conflicts/, plugins/, bundles/ and projects/. They stay on the
// machine and stay on disk; the vault travels in git without them, and the next
// machine rebuilds what it can from the canon, which does travel.
//
// The list is the vault's own .gitignore (machineLocalRules), so a rule added
// to that file is enforced here without a second edit, and a secret somebody
// adds to the promise is a secret this stops tracking.
//
// This is a migration, not a policy: a vault committed before these rules
// already tracks them, and `.gitignore` alone does not untrack a path. It runs
// once and is idempotent — a second run finds nothing tracked and says nothing.
//
// It returns the paths it untracked, so the caller can tell the user; git is
// the user's tool, so nothing is committed and nothing is deleted.
func (v *Vault) UntrackMachineLocal(ctx context.Context) ([]string, error) {
	if !v.inGitWorkTree(ctx) {
		return nil, nil
	}

	tracked := v.trackedMachineLocal(ctx)

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

// trackedMachineLocal lists the machine-local paths git currently tracks.
//
// The result is sorted because the paths become a note the user reads, and
// two machines untracking the same vault should say the same thing.
func (v *Vault) trackedMachineLocal(ctx context.Context) []string {
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

	for _, rule := range machineLocalRules {
		if tracked[rule] {
			found = append(found, rule)

			continue
		}

		// A directory rule covers the files inside it, so a tracked file under
		// one is the real case.
		prefix := strings.TrimSuffix(rule, "/") + "/"

		for name := range tracked {
			if strings.HasPrefix(name, prefix) {
				found = append(found, name)
			}
		}
	}

	slices.Sort(found)

	return found
}

// credentialPaths are the files in a vault that hold credential values: beadle's
// own store and the library's, because a vault is shared with verger and a
// committed value in either is a value somebody has to rotate.
var credentialPaths = []string{
	"mcp/secrets.json",
	"verger/state/secrets.json",
}

// CredentialExposure says where a credential file stands with git: not in the
// index and not in any commit is the only clean answer, and the two dirty ones
// need different words — one is fixed by a command beadle can run, the other
// by a decision only the user can make.
type CredentialExposure int

const (
	// CredentialAbsent means git has never heard of the file.
	CredentialAbsent CredentialExposure = iota

	// CredentialStaged means the file is in the index: not yet in the history,
	// but the next commit would carry it.
	CredentialStaged

	// CredentialCommitted means the file is in a commit, so its values are in
	// the history and stay there whatever happens to the index now.
	CredentialCommitted
)

// CredentialPaths returns the credential files in this vault, relative to its
// root, in git's terms.
func (v *Vault) CredentialPaths() []string {
	return slices.Clone(credentialPaths)
}

// CredentialExposure reports where path stands with git. A vault that is not a
// work tree is CredentialAbsent for every path: there is no history to have
// leaked into, and a warning about a repository that does not exist is noise
// that teaches a user to ignore the ones that matter.
func (v *Vault) CredentialExposure(ctx context.Context, path string) CredentialExposure {
	if !v.inGitWorkTree(ctx) {
		return CredentialAbsent
	}

	// Both questions, and the index first. A repository with a staged file and
	// no commits yet has a HEAD that does not resolve, and that is the state a
	// user's own `git init && git add -A` leaves behind — the one where the
	// next commit would carry the secret. Asking about the history first would
	// call that clean.
	staged := false

	if out, err := runGit(ctx, v.Root(), "ls-files", "-z", "--", path); err == nil && strings.TrimSpace(out) != "" {
		staged = true
	}

	committed := false

	if _, err := runGit(ctx, v.Root(), "rev-parse", "--verify", "HEAD"); err == nil {
		if out, err := runGit(ctx, v.Root(), "rev-list", "--all", "-1", "--", path); err == nil && strings.TrimSpace(out) != "" {
			committed = true
		}
	}

	switch {
	case committed:
		// The worse state wins: a path in a commit is in the history whatever
		// the index says now.
		return CredentialCommitted
	case staged:
		return CredentialStaged
	default:
		return CredentialAbsent
	}
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
