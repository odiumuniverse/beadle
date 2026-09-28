package cli

import (
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/bundle"
)

// hostResolutionEnv is every environment variable that steers beadle to a
// host's files: the roots a host reads (HOME, the XDG root, DSH_HOME,
// DSH_AGENTS_HOME, the omp root and profile keys) and PATH, which decides
// whether a host CLI is detected at all. pkg/daemon.UnitEnv documents the
// same list as the environment a watcher unit has to carry.
//
// The suite pins every one of them, so "the agents detected on this machine"
// means "the agents detected in an empty environment" and stays the same on a
// developer laptop and on a Linux box that has dsh installed.
var hostResolutionEnv = []string{
	"HOME",
	"XDG_CONFIG_HOME",
	"BEADLE_HOME",
	"DSH_HOME",
	"DSH_AGENTS_HOME",
	"PI_CONFIG_DIR",
	"PI_CODING_AGENT_DIR",
	"OMP_PROFILE",
	"PI_PROFILE",
	"BEADLE_ALLOW_HOME_MOVE",
	"PATH",
}

// envReadsThatDoNotResolveAnything are the variables the product reads that
// cannot redirect a host to a developer's files: a test toggle or a credential
// the caller supplies deliberately. Any other read must be pinned.
var envReadsThatDoNotResolveAnything = []string{
	"BEADLE_BUNDLE_CANARY",
	"BEADLE_DSH_E2E",
	"BEADLE_GIT_ORACLE",
	"BEADLE_KEYRING_E2E",
	"DEEPSEEK_API_KEY",
	"TMPDIR",
}

func TestNoHostBinaryOnTheSuitePath(t *testing.T) {
	Convey("Given the isolated test suite", t, func() {
		Convey("When a host CLI is looked up on PATH", func() {
			Convey("Then none of them resolves, so detection follows the test home alone", func() {
				binaries := []string{"dsh", "omp"}
				for _, host := range bundle.Hosts() {
					binaries = append(binaries, host.Binary())
				}

				for _, binary := range binaries {
					path, err := exec.LookPath(binary)
					So(err, ShouldNotBeNil)
					So(path, ShouldBeEmpty)
				}
			})
		})
	})
}

func TestHostResolutionEnvIsPinned(t *testing.T) {
	Convey("Given the isolated test suite", t, func() {
		Convey("When a test reads the environment", func() {
			Convey("Then every variable that steers host resolution is pinned", func() {
				home, err := os.UserHomeDir()
				So(err, ShouldBeNil)
				So(strings.HasPrefix(home, isolatedHome), ShouldBeTrue)

				for _, name := range hostResolutionEnv {
					switch name {
					case "PATH":
						So(os.Getenv(name), ShouldEqual, isolatedBinDir)
					case "HOME", "XDG_CONFIG_HOME", "BEADLE_HOME", "DSH_HOME":
						So(os.Getenv(name), ShouldContainSubstring, isolatedHome)
					default:
						// Pinned empty, so a developer's value cannot send a
						// host somewhere outside the test home.
						So(os.Getenv(name), ShouldBeEmpty)
					}
				}
			})
		})
	})
}

func TestProductEnvReadsArePinnedOrHarmless(t *testing.T) {
	Convey("Given the beadle product sources", t, func() {
		Convey("When every environment read is collected", func() {
			Convey("Then each one is either pinned by the suite or provably harmless", func() {
				for _, name := range productEnvReads(t) {
					if !slices.Contains(hostResolutionEnv, name) &&
						!slices.Contains(envReadsThatDoNotResolveAnything, name) {
						t.Errorf("%s is read by the product but pinned by no suite: add it to "+
							"hostResolutionEnv (it steers host resolution) or to "+
							"envReadsThatDoNotResolveAnything (it cannot redirect a host)", name)
					}
				}
			})
		})
	})
}

// productEnvReads returns the names the product reads from the environment
// through a literal os.Getenv/os.LookupEnv call, so a new read cannot join the
// code without either being pinned here or being declared harmless. The
// omp keys are read through constants in pkg/agent and are therefore part of
// the pinned list rather than of this scan.
func productEnvReads(t *testing.T) []string {
	t.Helper()

	seen := map[string]bool{}

	for _, dir := range productPackageDirs(t) {
		for _, name := range envReadsInDir(t, dir) {
			seen[name] = true
		}
	}

	return slices.Sorted(maps.Keys(seen))
}

func productPackageDirs(t *testing.T) []string {
	t.Helper()

	var dirs []string

	err := filepath.WalkDir("..", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			if entry.Name() == "testdata" || entry.Name() == "vendor" {
				return fs.SkipDir
			}

			return nil
		}

		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		dirs = append(dirs, filepath.Dir(path))

		return nil
	})
	So(err, ShouldBeNil)

	So(dirs, ShouldNotBeEmpty)

	slices.Sort(dirs)

	return slices.Compact(dirs)
}

func envReadsInDir(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	So(err, ShouldBeNil)

	var names []string

	for _, entry := range entries {
		file := entry.Name()
		if !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, file)) //nolint:gosec // G304: the test reads the repo's own sources
		So(err, ShouldBeNil)

		for line := range strings.SplitSeq(string(data), "\n") {
			for _, call := range []string{`os.Getenv("`, `os.LookupEnv("`} {
				_, rest, found := strings.Cut(line, call)
				if !found {
					continue
				}

				name, _, quoted := strings.Cut(rest, `"`)
				if quoted {
					names = append(names, name)
				}
			}
		}
	}

	return names
}
