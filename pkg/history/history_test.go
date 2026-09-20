package history_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
	"github.com/vmkteam/embedlog"

	"github.com/odiumuniverse/beadle/pkg/history"
)

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()

	all := append([]string{"-C", dir}, args...)

	out, err := exec.CommandContext(t.Context(), "git", all...).CombinedOutput() //nolint:gosec // G204: fixed git subcommands in tests
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}

	return strings.TrimSpace(string(out))
}

func initRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	gitOutput(t, dir, "init", "-q")

	return dir
}

func identitylessEnv(t *testing.T) {
	t.Helper()

	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryFallbackIdentity(t *testing.T) {
	Convey("Given a repository without any git identity", t, func() {
		identitylessEnv(t)

		dir := initRepo(t)
		gitOutput(t, dir, "config", "user.useConfigOnly", "true")
		writeFile(t, filepath.Join(dir, "a.md"), "a\n")

		Convey("When a history commit runs", func() {
			err := history.Commit(t.Context(), dir, "beadle: test", embedlog.Logger{})
			So(err, ShouldBeNil)

			Convey("Then the commit is authored and committed by beadle", func() {
				So(gitOutput(t, dir, "log", "-1", "--format=%an <%ae>"), ShouldEqual, "beadle <beadle@localhost>")
				So(gitOutput(t, dir, "log", "-1", "--format=%cn <%ce>"), ShouldEqual, "beadle <beadle@localhost>")
			})
		})
	})
}

func TestHistoryFallbackOutsideCLocale(t *testing.T) {
	Convey("Given a repository without identity in a non-C locale", t, func() {
		identitylessEnv(t)
		t.Setenv("LC_ALL", "fr_FR.UTF-8")

		dir := initRepo(t)
		gitOutput(t, dir, "config", "user.useConfigOnly", "true")
		writeFile(t, filepath.Join(dir, "a.md"), "a\n")

		Convey("When a history commit runs", func() {
			err := history.Commit(t.Context(), dir, "beadle: test", embedlog.Logger{})
			So(err, ShouldBeNil)

			Convey("Then the fallback still finds the identity error", func() {
				So(gitOutput(t, dir, "log", "-1", "--format=%an <%ae>"), ShouldEqual, "beadle <beadle@localhost>")
			})
		})
	})
}

func TestHistoryFallbackCommitterIdentity(t *testing.T) {
	Convey("Given an author from the environment but no committer identity", t, func() {
		identitylessEnv(t)
		t.Setenv("GIT_AUTHOR_NAME", "Env Author")
		t.Setenv("GIT_AUTHOR_EMAIL", "author@example.com")

		dir := initRepo(t)
		gitOutput(t, dir, "config", "user.useConfigOnly", "true")
		writeFile(t, filepath.Join(dir, "a.md"), "a\n")

		Convey("When a history commit runs", func() {
			err := history.Commit(t.Context(), dir, "beadle: test", embedlog.Logger{})
			So(err, ShouldBeNil)

			Convey("Then the committer is beadle", func() {
				So(gitOutput(t, dir, "log", "-1", "--format=%cn <%ce>"), ShouldEqual, "beadle <beadle@localhost>")
			})
		})
	})
}

func TestHistoryKeepsUserIdentity(t *testing.T) {
	Convey("Given a repository with a local git identity", t, func() {
		identitylessEnv(t)

		dir := initRepo(t)
		gitOutput(t, dir, "config", "user.name", "Test User")
		gitOutput(t, dir, "config", "user.email", "test@example.com")
		writeFile(t, filepath.Join(dir, "a.md"), "a\n")

		Convey("When a history commit runs", func() {
			err := history.Commit(t.Context(), dir, "beadle: test", embedlog.Logger{})
			So(err, ShouldBeNil)

			Convey("Then the user identity is used, not the fallback", func() {
				So(gitOutput(t, dir, "log", "-1", "--format=%an <%ae>"), ShouldEqual, "Test User <test@example.com>")
			})
		})
	})
}

func TestHistoryKeepsOtherErrors(t *testing.T) {
	Convey("Given a repository whose commit fails for another reason", t, func() {
		identitylessEnv(t)

		dir := initRepo(t)
		gitOutput(t, dir, "config", "user.name", "Test User")
		gitOutput(t, dir, "config", "user.email", "test@example.com")
		writeFile(t, filepath.Join(dir, "a.md"), "a\n")

		counter := filepath.Join(dir, "hook-runs")
		hook := filepath.Join(dir, ".git", "hooks", "pre-commit")
		writeFile(t, hook, "#!/bin/sh\nprintf 'run\\n' >> \"$COUNTER\"\nexit 1\n")
		So(os.Chmod(hook, 0o700), ShouldBeNil) //nolint:gosec // G302: an executable hook needs the execute bit
		t.Setenv("COUNTER", counter)

		Convey("When a history commit runs", func() {
			err := history.Commit(t.Context(), dir, "beadle: test", embedlog.Logger{})

			Convey("Then the original error surfaces and the fallback is not attempted", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "exit status")

				data, readErr := os.ReadFile(counter) //nolint:gosec // G304: test reads its own temp file
				So(readErr, ShouldBeNil)
				So(strings.Count(string(data), "run"), ShouldEqual, 1)
			})
		})
	})
}
