package agent_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// The pinned DSH release and its engines range. An older Node makes the
// launcher exit 0 with an empty output instead of failing, so the canary
// checks the version instead of trusting the exit code (Q-15).
const (
	dshCanaryVersion   = "0.1.5-rc.3"
	dshCanaryNodeMajor = 22
	dshCanaryNodeMinor = 19
	dshCanaryTimeout   = 10 * time.Minute
	dshCanarySkill     = "beadle-canary"
)

// dshCanaryGuard skips the canary unless the operator opted in and a key is
// present. Nothing — not even npm — is touched before the skip, so a keyless
// CI run stays green and offline.
func dshCanaryGuard(t *testing.T) {
	t.Helper()

	if os.Getenv("BEADLE_DSH_E2E") != "1" {
		t.Skip("set BEADLE_DSH_E2E=1 to run the DSH headless canary (installs the pinned npm package into a temp dir)")
	}

	if os.Getenv("DEEPSEEK_API_KEY") == "" {
		t.Skip("DEEPSEEK_API_KEY is not set: the DSH headless run would fail with MISSING_CREDENTIAL")
	}

	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not found")
	}
}

// dshCanaryMarker returns a random token the model can only know from the
// fixture: a hallucinated answer cannot match it.
func dshCanaryMarker(t *testing.T) string {
	t.Helper()

	buf := make([]byte, 8)

	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("random marker: %v", err)
	}

	return "beadle-dsh-canary-" + hex.EncodeToString(buf)
}

// dshCanaryNodeOK reports whether a Node satisfies the engines range of the
// pinned package: >=22.19.0.
func dshCanaryNodeOK(t *testing.T, node string) bool {
	t.Helper()

	out, err := exec.CommandContext(t.Context(), node, "--version").Output()
	if err != nil {
		return false
	}

	version := strings.TrimSpace(string(out))

	major, minor, ok := dshCanaryParseNode(version)
	if !ok {
		return false
	}

	return major > dshCanaryNodeMajor || (major == dshCanaryNodeMajor && minor >= dshCanaryNodeMinor)
}

// dshCanaryParseNode parses a "v<major>.<minor>…" version string.
func dshCanaryParseNode(version string) (major, minor int, ok bool) {
	parts := strings.Split(strings.TrimPrefix(version, "v"), ".")

	if len(parts) < 2 {
		return 0, 0, false
	}

	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}

	minor, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, false
	}

	return major, minor, true
}

// dshCanarySetup installs the pinned DSH into a temp npm prefix and returns
// the launcher, the directory holding a supported Node and the workspace the
// run starts in. The system Node is used when it satisfies the engines range;
// otherwise node@24 is installed into the same temp prefix.
func dshCanarySetup(t *testing.T, work string) (launcher, nodeDir, workspace string) {
	t.Helper()

	prefix := filepath.Join(work, "npm")
	packages := []string{"@deepseek-ai/dsh@" + dshCanaryVersion}

	node := ""

	if path, err := exec.LookPath("node"); err == nil && dshCanaryNodeOK(t, path) {
		node = path
	} else {
		packages = append(packages, "node@24")
	}

	args := append([]string{"install", "--prefix", prefix, "--save-exact", "--no-audit", "--no-fund"}, packages...)

	cmd := exec.CommandContext(t.Context(), "npm", args...) //nolint:gosec // G204: a fixed npm subcommand with pinned package names

	cmd.Env = append(os.Environ(), "npm_config_update_notifier=false")

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("npm install %s: %v\n%s", strings.Join(packages, " "), err, out)
	}

	bin := filepath.Join(prefix, "node_modules", ".bin")

	if node == "" {
		node = filepath.Join(bin, "node")
	}

	workspace = filepath.Join(work, "workspace")

	if err := os.MkdirAll(workspace, 0o750); err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	return filepath.Join(bin, "dsh"), filepath.Dir(node), workspace
}

// dshCanaryRun runs one headless task against a disposable HOME, DSH_HOME and
// workspace. The child environment carries nothing but the paths it needs, so
// the run cannot read the machine's own DSH configuration; the key is passed
// only when key is true (the keyless case must fail closed).
func dshCanaryRun(t *testing.T, launcher, nodeDir, home, dshHome, workspace string, key bool, task string) (string, string, int) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), dshCanaryTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, launcher, "--profile", "headless", task) //nolint:gosec // G204: the launcher is the pinned package installed into the test's temp prefix
	cmd.Dir = workspace
	cmd.Env = []string{
		"HOME=" + home,
		"DSH_HOME=" + dshHome,
		"PATH=" + nodeDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"NO_COLOR=1",
	}

	if key {
		cmd.Env = append(cmd.Env, "DEEPSEEK_API_KEY="+os.Getenv("DEEPSEEK_API_KEY"))
	}

	var stdout, stderr bytes.Buffer

	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	err := cmd.Run()

	if ctx.Err() != nil {
		t.Fatalf("dsh headless run timed out after %s", dshCanaryTimeout)
	}

	code := 0

	var exitErr *exec.ExitError

	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		t.Fatalf("dsh headless run: %v", err)
	}

	return stdout.String(), stderr.String(), code
}

// dshCanaryFixture lays out the beadle-style DSH home the canary asks about:
// the user-global AGENTS.md and one skill, each carrying a random marker.
func dshCanaryFixture(t *testing.T, work string) (home, dshHome, marker, token string) {
	t.Helper()

	home = filepath.Join(work, "home")
	dshHome = filepath.Join(work, "dsh")
	marker = dshCanaryMarker(t)
	token = dshCanaryMarker(t)

	writeFile(t, filepath.Join(home, ".keep"), "")
	writeFile(t, filepath.Join(dshHome, "AGENTS.md"),
		"# canary instructions\n\nWhen asked for the CANARY-MARKER line, answer with the line below verbatim.\n\nCANARY-MARKER: "+marker+"\n")
	writeFile(t, filepath.Join(dshHome, "skills", dshCanarySkill, "SKILL.md"),
		"---\nname: "+dshCanarySkill+"\ndescription: beadle canary skill; it carries a token\n---\n\n# "+dshCanarySkill+"\n\nThe token is "+token+"\n")

	return home, dshHome, marker, token
}

// dshCanaryNote records the run's streams: Go prints them with a failing test,
// and the model's reasoning arrives on stderr, so a red canary is readable
// without a re-run.
func dshCanaryNote(t *testing.T, stdout, stderr string) {
	t.Helper()

	t.Logf("dsh headless stdout:\n%s", dshCanaryClip(stdout))
	t.Logf("dsh headless stderr:\n%s", dshCanaryClip(stderr))
}

// dshCanaryClip bounds a stream so a long reasoning trace stays readable.
func dshCanaryClip(value string) string {
	const limit = 2000

	if len(value) <= limit {
		return value
	}

	return value[:limit] + "… (truncated)"
}

// TestDSHCanaryHeadless runs the pinned DSH headless profile against a
// disposable DSH_HOME and checks the contract Q-15 recorded live: the final
// answer arrives on stdout, exit 0 means success, this RC rejects --json, and
// a keyless run fails closed with exit 1. The canary is opt-in
// (BEADLE_DSH_E2E=1 + DEEPSEEK_API_KEY) and never touches the machine's own
// home or DSH configuration.
func TestDSHCanaryHeadless(t *testing.T) {
	dshCanaryGuard(t)

	Convey("Given a disposable DSH_HOME with beadle-style rules and a skill", t, func() {
		work := t.TempDir()

		home, dshHome, marker, token := dshCanaryFixture(t, work)

		launcher, nodeDir, workspace := dshCanarySetup(t, work)

		Convey("When the headless profile answers a two-part task", func() {
			stdout, stderr, code := dshCanaryRun(t, launcher, nodeDir, home, dshHome, workspace, true,
				"Two questions. (1) Reply with the CANARY-MARKER line from your instructions verbatim. "+
					"(2) Read the "+dshCanarySkill+" skill and reply with its token verbatim. "+
					"Answer with those two values only.")

			Convey("Then the answer lands on stdout and the run exits 0", func() {
				dshCanaryNote(t, stdout, stderr)

				So(code, ShouldEqual, 0)

				// An unsupported Node exits 0 with an empty output: the exit
				// code alone is not trusted, the stdout must carry the answer.
				So(strings.TrimSpace(stdout), ShouldNotBeEmpty)
				So(stdout, ShouldContainSubstring, marker)
				So(stdout, ShouldContainSubstring, token)
			})
		})

		Convey("When the profile is asked for the removed --json flag", func() {
			stdout, stderr, code := dshCanaryRun(t, launcher, nodeDir, home, dshHome, workspace, true, "--json")

			Convey("Then this RC rejects it with exit 1: the NDJSON contract is not in 0.1.5-rc.3", func() {
				dshCanaryNote(t, stdout, stderr)

				So(code, ShouldEqual, 1)
				So(stderr, ShouldContainSubstring, "unknown option")
			})
		})

		Convey("When the run has no key", func() {
			stdout, stderr, code := dshCanaryRun(t, launcher, nodeDir, home, dshHome, workspace, false, "say hi")

			Convey("Then it fails closed with exit 1 and MISSING_CREDENTIAL", func() {
				dshCanaryNote(t, stdout, stderr)

				So(code, ShouldEqual, 1)
				So(stderr, ShouldContainSubstring, "MISSING_CREDENTIAL")
			})
		})
	})
}
