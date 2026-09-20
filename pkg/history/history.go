package history

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/vmkteam/embedlog"
)

func Commit(ctx context.Context, dir, message string, log embedlog.Logger) error {
	if _, err := exec.LookPath("git"); err != nil {
		log.Error(ctx, "git history is enabled but git is not installed")

		return nil //nolint:nilerr // a missing git must not fail the sync itself
	}

	if err := ensureRepo(ctx, dir); err != nil {
		return err
	}

	if err := runGit(ctx, dir, "add", "-A"); err != nil {
		return err
	}

	if clean, err := nothingStaged(ctx, dir); err != nil {
		return err
	} else if clean {
		return nil
	}

	if err := commit(ctx, dir, message); err != nil {
		return err
	}

	log.Print(ctx, "vault history committed", "message", message)

	return nil
}

var identityMarkers = []string{
	"Author identity unknown",
	"Committer identity unknown",
	"empty ident",
	"unable to auto-detect email",
}

func commit(ctx context.Context, dir, message string) error {
	err := runGit(ctx, dir, "commit", "-q", "-m", message)
	if err == nil {
		return nil
	}

	if !identityFailure(err) {
		return err
	}

	return runGitWith(ctx, dir, identityEnv(), "commit", "-q", "-m", message)
}

func identityEnv() []string {
	return append(os.Environ(),
		"LC_ALL=C",
		"GIT_AUTHOR_NAME=beadle",
		"GIT_AUTHOR_EMAIL=beadle@localhost",
		"GIT_COMMITTER_NAME=beadle",
		"GIT_COMMITTER_EMAIL=beadle@localhost",
	)
}

func identityFailure(err error) bool {
	for _, marker := range identityMarkers {
		if strings.Contains(err.Error(), marker) {
			return true
		}
	}

	return false
}

func ensureRepo(ctx context.Context, dir string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return nil
	}

	return runGit(ctx, dir, "init", "-q")
}

func nothingStaged(ctx context.Context, dir string) (bool, error) {
	err := runGit(ctx, dir, "diff", "--cached", "--quiet")
	if err == nil {
		return true, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}

	return false, err
}

func runGit(ctx context.Context, dir string, args ...string) error {
	return runGitWith(ctx, dir, gitEnv(), args...)
}

func gitEnv() []string {
	return append(os.Environ(), "LC_ALL=C")
}

func runGitWith(ctx context.Context, dir string, env []string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // G204: fixed git subcommands only
	cmd.Dir = dir
	cmd.Env = env

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}

	return nil
}
