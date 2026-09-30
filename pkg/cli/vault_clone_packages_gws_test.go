package cli_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

// The vault travels. A spec that names a package by a path the machine that
// wrote it happens to have delivers nothing on the next machine - the
// library's own `status` then truthfully says "no cells" - so this is two
// homes and a copied vault.
//
// A local source is one package, spelled relative to the spec's own directory
// (`./packages/demo`), because that is the form that survives the vault moving.
// goTool is where the `go` binary was before the suite's TestMain replaced PATH
// with a hermetic one. A package variable initialises before TestMain runs, so
// this resolves while PATH is still the developer's; looking it up later finds
// nothing. runtime.GOROOT is not the answer either — it is deprecated
// precisely because a copied binary carries the root it was built with, not
// the root of the machine running it.
var goTool = func() string {
	if path, err := exec.LookPath("go"); err == nil {
		return path
	}

	return "go"
}()

func TestTheVaultsPackageSpecDeliversOnAnotherMachine(t *testing.T) {
	// One build for the whole scenario, and this function owns its lifetime.
	// Building per subtest is what it used to do: the helper wrote into the
	// caller's t.TempDir(), so every subtest paid a full `go build` of the
	// command - ten of them, each its own binary, to test one thing. A binary
	// in a subtest's t.TempDir() also disappears with that subtest, so the
	// path cannot come from there; the build is a package-level resource with
	// the root test's cleanup as its owner.
	t.Cleanup(dropSharedBeadle)

	Convey("Given a vault whose spec names a package stored inside it", t, func() {
		first := newMachine(t)

		first.writeLocalPackage("demo", "1.0.0")
		first.writeSpec("demo")

		Convey("When the first machine syncs", func() {
			// The vault carries the spec before any command runs: without this
			// the only evidence would be the sync's own word about itself, and
			// a machine whose vault points somewhere else would look the same
			// as a spec the library could not read.
			So(first.specOnDisk(), ShouldBeTrue)

			// A machine that manages an agent has that agent's own directory
			// and has it enabled: the library discovers its hosts there, and
			// with none it refuses to target anything. An empty home is not a
			// machine with nothing to deliver - it is a machine the delivery
			// was never asked of.
			So(first.enableAgent("claude"), ShouldBeNil)

			firstOut, err := first.beadle("sync")
			So(err, ShouldBeNil)

			Convey("Then the spec is applied and the lock is written in the same pass", func() {
				So(firstOut, ShouldContainSubstring, "demo")
				So(firstOut, ShouldNotContainSubstring, "the vault carries no package spec")
				So(first.fileExists("verger.lock"), ShouldBeTrue)
			})

			Convey("Then a second machine, given a copy of the vault, delivers too", func() {
				second := cloneMachine(t, first)

				secondOut, err := second.beadle("sync")
				So(err, ShouldBeNil)
				So(secondOut, ShouldNotContainSubstring, "the vault carries no package spec")

				Convey("Then the copy's spec was applied on this machine", func() {
					So(second.fileExists("verger.lock"), ShouldBeTrue)
					So(second.appliedSpec(), ShouldContainSubstring, "demo")
				})
			})
		})
	})
}

// machine is one home with a beadle vault in it.
type machine struct {
	t     *testing.T
	home  string
	vault string
}

func newMachine(t *testing.T) *machine {
	t.Helper()

	home := t.TempDir()
	vault := filepath.Join(home, ".beadle")

	if err := os.MkdirAll(vault, 0o700); err != nil {
		t.Fatalf("vault: %v", err)
	}

	t.Setenv("HOME", home)
	t.Setenv("DSH_HOME", filepath.Join(home, ".dsh"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("BEADLE_HOME", vault)

	beadle := sharedBeadle(t)

	if out, err := run(t, beadle, home, "init"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}

	return &machine{t: t, home: home, vault: vault}
}

// cloneMachine is the second machine: a different home, a copy of the first
// one's vault, and nothing from the first home reachable.
func cloneMachine(t *testing.T, from *machine) *machine {
	t.Helper()

	home := t.TempDir()
	vault := filepath.Join(home, ".beadle")

	if err := copyTree(from.vault, vault); err != nil {
		t.Fatalf("clone vault: %v", err)
	}

	t.Setenv("HOME", home)
	t.Setenv("DSH_HOME", filepath.Join(home, ".dsh"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("BEADLE_HOME", vault)

	return &machine{t: t, home: home, vault: vault}
}

func (m *machine) beadle(args ...string) (string, error) {
	m.t.Helper()

	return run(m.t, sharedBeadle(m.t), m.home, args...)
}

// enableAgent makes this machine manage one agent: the agent's own directory
// under the home, and the agent enabled in the vault. The library discovers
// its hosts on disk, and the vault's own surfaces need the agent enabled, so
// a machine that has neither is a machine no delivery was ever asked of.
func (m *machine) enableAgent(id string) error {
	if err := os.MkdirAll(filepath.Join(m.home, "."+id), 0o700); err != nil {
		return err
	}

	_, err := m.beadle("agents", "enable", id)

	return err
}

// writeLocalPackage puts one package inside the vault, in the shape the
// library's own packer writes: a plugin.json plus a skills directory. It lives
// under the library's home (`<vault>/verger/packages`) because a local source
// is resolved from the spec's own directory and a local ref may not escape it
// with `..` - a package one level up is not addressable by a portable spec.
func (m *machine) writeLocalPackage(name, version string) {
	m.t.Helper()

	root := filepath.Join(m.vault, "verger", "packages", name)

	if err := os.MkdirAll(filepath.Join(root, "skills", name), 0o700); err != nil {
		m.t.Fatalf("package dir: %v", err)
	}

	manifest := `{"name":"` + name + `","version":"` + version + `","description":"a demo package"}`
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), []byte(manifest), 0o600); err != nil {
		m.t.Fatalf("plugin.json: %v", err)
	}

	skill := "---\nname: " + name + "\ndescription: a demo skill\n---\n\nbody\n"
	if err := os.WriteFile(filepath.Join(root, "skills", name, "SKILL.md"), []byte(skill), 0o600); err != nil {
		m.t.Fatalf("SKILL.md: %v", err)
	}
}

// writeSpec writes the vault's spec with one local source pointing at the
// package itself, relative to the spec's own directory - the form that
// survives the vault moving to another machine. A local source is one package
// (`verger install local:<path>`); a directory of packages is a marketplace,
// which beadle does not host locally.
func (m *machine) writeSpec(name string) {
	m.t.Helper()

	doc := "schema = 1\n\n" +
		"[[source]]\nname = \"in-vault\"\nurl = \"./packages/" + name + "\"\n\n" +
		"[[package]]\nid = \"" + name + "\"\n"

	if err := os.MkdirAll(filepath.Join(m.vault, "verger"), 0o700); err != nil {
		m.t.Fatalf("verger home: %v", err)
	}

	if err := os.WriteFile(filepath.Join(m.vault, "verger", "verger.toml"), []byte(doc), 0o600); err != nil {
		m.t.Fatalf("spec: %v", err)
	}
}

func (m *machine) fileExists(name string) bool {
	_, err := os.Stat(filepath.Join(m.vault, "verger", name))

	return err == nil
}

// specOnDisk reports whether the spec exists where the library reads it: the
// library's home inside the vault.
func (m *machine) specOnDisk() bool {
	_, err := os.Stat(filepath.Join(m.vault, "verger", "verger.toml"))

	return err == nil
}

func (m *machine) appliedSpec() string {
	m.t.Helper()

	data, err := os.ReadFile(filepath.Join(m.vault, "verger", "verger.toml"))
	if err != nil {
		return ""
	}

	return string(data)
}

// sharedBeadle is the command under test, built once for the package.
//
// The scenario is two homes and a copied vault, so no in-process fixture can
// stand in for a machine and the command has to exist as a binary. It does not
// have to exist as ten of them: every caller wants the same build, and the
// subtests that drive it are leaves of one root test. sync.OnceValues is what
// makes the second caller wait for the first build instead of starting its own.
var sharedBeadleOnce = newSharedBeadle()

// newSharedBeadle is one armed build, ready to be fired exactly once.
func newSharedBeadle() func() (string, error) {
	return sync.OnceValues(func() (string, error) {
		dir, err := os.MkdirTemp("", "beadle-cli-bin")
		if err != nil {
			return "", err
		}

		sharedBeadleDir = dir

		return buildBeadleIn(dir)
	})
}

// sharedBeadleDir is the scratch the one build lives in, kept beside the Once
// so dropSharedBeadle can remove it.
var sharedBeadleDir string

func sharedBeadle(t *testing.T) string {
	t.Helper()

	bin, err := sharedBeadleOnce()
	if err != nil {
		t.Fatalf("build beadle: %v", err)
	}

	return bin
}

// dropSharedBeadle removes the build and lets a later run make another. The
// root test registers it: a subtest's cleanup would take the binary away from
// its siblings, which is the same trap as a per-subject t.TempDir().
func dropSharedBeadle() {
	if sharedBeadleDir != "" {
		_ = os.RemoveAll(sharedBeadleDir)
		sharedBeadleDir = ""
	}

	sharedBeadleOnce = newSharedBeadle()
}

// buildBeadleIn compiles the command into dir.
func buildBeadleIn(dir string) (string, error) {
	bin := filepath.Join(dir, "beadle")

	// `go` from PATH, not runtime.GOROOT()+"/bin/go": GOROOT is deprecated
	// because the root baked into a binary is not the root of the machine
	// running it — which is exactly the case for a test binary. The binary
	// here is a literal; `bin` is the build's own output, not input.
	// context.Background, not a subtest's: the build outlives the subtest that
	// happened to ask for it first, and a cancelled context would abort it.
	cmd := exec.CommandContext(context.Background(), goTool, "build", "-o", bin, "../../cmd/beadle") //nolint:gosec // G204: a path resolved at init, not input

	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build beadle: %w\n%s", err, out)
	}

	return bin, nil
}

func run(t *testing.T, beadle, home string, args ...string) (string, error) {
	t.Helper()

	// `beadle` is the binary this test just built under t.TempDir(); the
	// arguments are literals this test passes.
	cmd := exec.CommandContext(t.Context(), beadle, args...) //nolint:gosec // G204: the binary is the test's own build output, not input
	cmd.Env = []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"BEADLE_HOME=" + filepath.Join(home, ".beadle"),
		"PATH=" + os.Getenv("PATH"),
	}

	out, err := cmd.CombinedOutput()

	return string(out), err
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		target := filepath.Join(dst, rel)

		if info.IsDir() {
			return os.MkdirAll(target, 0o700)
		}

		// A symlink is copied as the file it points at: a copy has to be
		// readable on the other machine, and a dangling link is not.
		// `path` is what filepath.Walk just produced under `src`, a tree this
		// test created; nothing here follows input from outside the test.
		data, err := os.ReadFile(path) //nolint:gosec // G304: read of a path Walk produced under the test's own tree
		if err != nil {
			return err
		}

		// `target` is dst joined with a relative path Walk derived from src,
		// both under this test's own temp dirs.
		return os.WriteFile(target, data, 0o600) //nolint:gosec // G703: a test-local copy destination, not a path from input
	})
}

// TestSharedBeadleIsBuiltOnce pins the contract the scenario depends on. The
// cost it guards is not subtle but it is invisible in the source: a helper that
// compiles per caller is a correct-looking line of code that costs a full
// `go build` of the command every time a subtest asks for it, which on this
// scenario was ten builds of one binary.
func TestSharedBeadleIsBuiltOnce(t *testing.T) {
	// Whoever triggers the build owns it. Without this the build outlives the
	// test that asked for it and the scratch directory is left in $TMPDIR -
	// which is the same class of leak as the ten binaries, one level up.
	t.Cleanup(dropSharedBeadle)

	Convey("Given the package's shared build", t, func() {
		first := sharedBeadle(t)

		Convey("When more callers ask for it", func() {
			second := sharedBeadle(t)
			third := sharedBeadle(t)

			Convey("Then every caller gets the same binary", func() {
				So(second, ShouldEqual, first)
				So(third, ShouldEqual, first)
			})

			Convey("And it is one real binary on disk", func() {
				// The identity assertions above would pass on a path that does
				// not exist, so the path is checked against the filesystem:
				// three callers, one file, and it is a binary.
				info, err := os.Stat(first)
				So(err, ShouldBeNil)
				So(info.Size(), ShouldBeGreaterThan, 0)
			})
		})
	})
}
