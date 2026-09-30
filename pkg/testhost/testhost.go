// Package testhost makes an agent host look installed to a test, without
// installing anything.
//
// beadle decides whether an agent is there with `exec.LookPath` on the host's
// own CLI, and a CI box, a container, or a hermetic run whose PATH is just
// `go` and `git` has none of them. A test that needs a host then fails for a
// reason that has nothing to do with what it is testing: `beadle plugins list`
// finds no host, `beadle sync` delivers to nothing, and the assertion that
// mattered never runs.
//
// This writes an inert stub executable for every host CLI the shared host table
// declares and puts that directory in front of PATH. The list is read from
// `pkg/hostpath`, not written out here, so a host that gains a CLI — or renames
// one — is covered with no change to this file.
//
// From an ordinary test:
//
//	testhost.Stubs(t)
//
// From a TestMain, which has no *testing.T to set an env var through:
//
//	os.Setenv("PATH", testhost.Dir(t)+string(os.PathListSeparator)+os.Getenv("PATH"))
//
// The stubs are inert: they exit 0 and print nothing. A stub that answered a
// real question would make the test depend on the stub's answers, which is
// worse than having no host at all.
package testhost

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/odiumuniverse/beadle/pkg/agentid"

	"github.com/odiumuniverse/verger/pkg/hostpath"
)

// stub is what every stubbed CLI does: answer an empty JSON document and
// succeed. An empty document rather than silence, because verger's oracle runs
// the CLI and parses what it prints — a stub that printed nothing made the
// oracle report "unexpected end of JSON input", which is a question about the
// stub and not about the code under test.
const stub = "#!/bin/sh\nprintf '{}\\n'\nexit 0\n"

// Binaries is every host CLI beadle knows how to look for, deduplicated and
// sorted.
func Binaries() []string {
	seen := map[string]bool{}

	// `Home` is a throwaway: which binaries a host looks for is a property of
	// the host, not of the machine the test happens to run on. Resolving real
	// surfaces here would put the developer's home back into the answer.
	home, err := os.MkdirTemp("", "testhost-home")
	if err != nil {
		return nil
	}

	defer func() { _ = os.RemoveAll(home) }()

	env := hostpath.Env{Home: home, GOOS: runtime.GOOS, Lookup: os.LookupEnv}

	for _, id := range hostpath.All() {
		resolved, surfaceErr := hostpath.Surfaces(id, env)
		if surfaceErr != nil {
			continue
		}

		for _, name := range resolved.Markers.Binaries {
			seen[name] = true
		}
	}

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}

	slices.Sort(names)

	return names
}

// Stubs puts an executable stub on PATH for every host CLI beadle knows, and
// returns the directory holding them.
//
// Safe to call from several tests: each gets its own directory and PATH grows
// by one entry. A test that needs a DIFFERENT answer — that a host is absent,
// say — should set PATH itself and not call this.
func Stubs(t *testing.T) string {
	t.Helper()

	dir := Dir(t)

	// The existing PATH is kept, not replaced: a hermetic harness puts `go`
	// and `git` there, and a suite that lost them would fail to build a binary
	// rather than to say what it meant.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return dir
}

// Dir writes the stubs into a fresh directory and returns it WITHOUT touching
// PATH, for a TestMain that sets the environment itself.
func Dir(tb testing.TB) string {
	tb.Helper()

	dir := tb.TempDir()

	for _, name := range Binaries() {
		write(tb, dir, name)
	}

	return dir
}

// Wrap installs stubs for the named CLIs only. A test that says it is about
// one host should say so in its setup, so a failure cannot turn out to be about
// a second host it never meant to have.
func Wrap(tb testing.TB, names ...string) string {
	tb.Helper()

	dir := tb.TempDir()

	for _, name := range names {
		write(tb, dir, name)
	}

	tb.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return dir
}

// DirInto writes the stubs into a directory the caller already owns and
// returns their names. It is the seam for a TestMain that builds its own bin
// directory and PATH — pkg/cli's isolation does exactly that — where Dir's
// `testing.TB` cannot be satisfied.
func DirInto(dir string) ([]string, error) {
	names := Binaries()

	for _, name := range names {
		// 0o700, not 0o600: the whole point is that `exec.LookPath` finds and
		// runs it, and the exec bit is what makes that true. A stub written
		// 0o600 is a file the test cannot use.
		if err := os.WriteFile(filepath.Join(dir, name), []byte(stub), 0o700); err != nil { //nolint:gosec // G306: the exec bit is the requirement
			return names, err
		}
	}

	return names, nil
}

// ConfigDirs makes the named hosts present the other way a host is found: by
// their own config directory. The binaries cover the hosts the shared table
// names a CLI for, but a host like Claude Code is identified by `~/.claude`,
// so a test that needs one has to make that directory too. The path comes from
// the same table, so this follows a host's home wherever it moves.
//
// Both halves are needed: a stubbed CLI with no config is "installed, but no
// config found", and a config with no CLI is present as well. Which one a
// test wants is its own choice; Absent and a bare temp HOME are the other.
func ConfigDirs(tb testing.TB, ids ...string) []string {
	tb.Helper()

	home, err := os.UserHomeDir()
	if err != nil {
		tb.Fatalf("testhost: resolve home: %v", err)
	}

	env := hostpath.Env{Home: home, GOOS: runtime.GOOS, Lookup: os.LookupEnv}

	made := make([]string, 0, len(ids))

	for _, id := range ids {
		roots, rootErr := hostpath.Roots(agentid.Canonical(id), env)
		if rootErr != nil {
			tb.Fatalf("testhost: roots for %s: %v", id, rootErr)
		}

		if roots.ConfigRoot == "" {
			continue
		}

		if mkErr := os.MkdirAll(roots.ConfigRoot, 0o700); mkErr != nil {
			tb.Fatalf("testhost: make config root for %s: %v", id, mkErr)
		}

		made = append(made, roots.ConfigRoot)
	}

	return made
}

func write(tb testing.TB, dir, name string) {
	tb.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(stub), 0o700); err != nil { //nolint:gosec // G306: as above — the stub must be executable
		tb.Fatalf("testhost: write stub for %s: %v", name, err)
	}
}
