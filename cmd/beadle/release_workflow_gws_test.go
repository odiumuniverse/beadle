package main

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
	"go.yaml.in/yaml/v3"
)

// The publish job computes the three digests the formula needs, and a pipeline's
// exit status is the exit status of its *last* command. The digests are read as
// `sha256sum "dist/…" | cut -d' ' -f1`, so a missing artefact makes sha256sum
// fail, leaves cut exiting 0, and assigns an empty string — and the release goes
// on to commit a formula whose sha256 is "". Homebrew then fails much later, on
// a user's machine, with an error that points at the tap rather than at a
// tarball that was never built.
//
// The step is checked by running it, not by reading it: the digest half of the
// real step, extracted with a YAML parser, against a dist/ that has nothing in
// it. It has to fail, and it has to say which file it wanted.
func TestTheFormulaStepRefusesToRenderWithoutItsArtifacts(t *testing.T) {
	Convey("Given the publish step's digest half and a dist with no artifacts in it", t, func() {
		script := digestHalf(t, "../../.github/workflows/release.yml")

		home := t.TempDir()
		if err := os.MkdirAll(filepath.Join(home, "dist"), 0o700); err != nil {
			t.Fatal(err)
		}

		path := filepath.Join(home, "digest.sh")
		if err := os.WriteFile(path, []byte("set -e\n"+script), 0o600); err != nil {
			t.Fatal(err)
		}

		// `sh path`, not `path`: the runner's step is a shell script, and
		// handing it to sh needs no executable bit, so the file can be written
		// 0600 like everything else a test writes.
		cmd := exec.CommandContext(t.Context(), "sh", path) //nolint:gosec // G204: `sh` is a literal and path is this test's own extraction
		cmd.Dir = home
		// The step refuses early without a token, which is its own contract and
		// not what this test is about; a dummy gets past it so the digests run.
		cmd.Env = append(os.Environ(), "VERSION=0.5.0", "TAP_TOKEN=dummy")

		out, err := cmd.CombinedOutput()

		Convey("Then the step fails instead of writing an empty digest into the formula", func() {
			So(err, ShouldNotBeNil)
			// Whichever artefact it notices first, it has to name it: "something
			// is missing" is the difference between a release that stops here and
			// one that ships sha256 "".
			So(string(out), ShouldContainSubstring, ".tar.gz is missing")
			So(string(out), ShouldContainSubstring, "::error::")
		})

		Convey("Then it never reaches the tap", func() {
			So(string(out), ShouldNotContainSubstring, "Cloning into")
		})
	})
}

// digestHalf is the part of the "Bump the formula in homebrew-tap" step that runs
// before it clones anything: the version, the token check and the three digests.
// Slicing there is what keeps the test away from the network and from the push —
// the failure this is about happens entirely before either.
func digestHalf(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // G304: the workflow this test checks
	if err != nil {
		t.Fatal(err)
	}

	var doc struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}

	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("release.yml is not valid YAML: %v", err)
	}

	publish, ok := doc.Jobs["publish"]
	if !ok {
		t.Fatal("release.yml has no publish job")
	}

	for _, step := range publish.Steps {
		if step.Name != "Bump the formula in homebrew-tap" {
			continue
		}

		lines := strings.Split(step.Run, "\n")

		for i, line := range lines {
			if strings.Contains(line, "git clone") {
				return strings.Join(lines[:i], "\n")
			}
		}

		t.Fatal("the step no longer clones; the harness cannot tell where the digests end")
	}

	t.Fatal("release.yml has no step named \"Bump the formula in homebrew-tap\"")

	return ""
}

// The contract a user actually relies on: `shasum -c checksums.txt` must pass
// against the files the release page offers. That holds only if the checksummed
// set and the published set are the same set — and, more weakly but just as
// visibly, if every tarball the build creates is in that set at all.
//
// The defect this pins: a tarball is built into dist/ and swept up by a broad
// `shasum ./*.tar.gz`, but no upload step publishes it. The release ships a
// checksums.txt naming a file nobody can download, and the verification every
// release tells the user to run fails on that one line.
//
// The workflow now declares its artifacts once, in the "List the release
// artifacts" step, and both the checksum step and the upload steps read that
// declaration back through its step outputs. So the test resolves the
// indirection rather than pattern-matching the raw YAML: the published set and
// the checksummed set are each computed by following their references to that
// one list, and a second hand-maintained list cannot appear without this
// noticing.
func TestTheChecksumsCoverExactlyThePublishedArtifacts(t *testing.T) {
	Convey("Given the release workflow's build, artifact-list, checksum and upload steps", t, func() {
		doc := readReleaseWorkflow(t, "../../.github/workflows/release.yml")

		built := builtArchives(doc)
		declared := declaredOutputNames(doc)
		checksummed := resolveReferences(doc, checksumOperands(doc), built)
		published := resolveReferences(doc, uploadPaths(doc), built)

		// checksums.txt is published alongside the artifacts and is the one file
		// that cannot appear inside itself: a checksum of the file is only
		// computable after the file is complete, so a line for it is impossible
		// by construction. `shasum -c` reads the file it is checking against, so
		// a line naming checksums.txt would be unsatisfiable, not merely
		// redundant. It is excluded from both sides for that reason and not
		// because it is inconvenient, and it is asserted to be published below
		// so the exclusion cannot become a way to stop shipping it.
		const checksumsFile = "checksums.txt"

		artifacts := without(published, checksumsFile)

		// An empty set on either side makes the equality below true for the
		// wrong reason, so it is asserted before it is relied on: a parser that
		// silently stopped finding steps would otherwise turn this test into a
		// no-op that always passes.
		So(built, ShouldNotBeEmpty)
		So(declared, ShouldNotBeEmpty)
		So(checksummed, ShouldNotBeEmpty)
		So(artifacts, ShouldNotBeEmpty)

		Convey("Then every tarball the build creates is one of the declared artifacts", func() {
			So(built, ShouldResemble, sortedValues(declared))
		})

		Convey("Then the checksums file is still published, since a user needs it to verify anything", func() {
			So(published, ShouldContain, checksumsFile)
		})

		Convey("Then every checksummed file is actually uploaded to the release", func() {
			for _, name := range checksummed {
				So(artifacts, ShouldContain, name)
			}
		})

		Convey("Then every uploaded artifact carries a checksum", func() {
			for _, name := range artifacts {
				So(checksummed, ShouldContain, name)
			}
		})

		Convey("Then the two sets are identical", func() {
			So(checksummed, ShouldResemble, artifacts)
		})
	})
}

// shasumArgsWithValue are the checksum tool's options that take their argument as
// a separate word. A token splitter that only dropped tokens beginning with "-"
// would read the algorithm in `shasum -a 256 …` as a file to checksum, and then
// compare a literal "256" against real artifact names.
var shasumArgsWithValue = map[string]bool{
	"-a": true, "--algorithm": true,
	"-b": true, "--binary": true,
	"-t": true, "--text": true,
	"-U": true, "--untag": true,
	"-C": true, "--tag": true,
}

var (
	// `tar <flags> <path>`: the archive a build step creates. The flag bundle is
	// captured so a listing call can be told apart from a creation.
	tarCreateRe = regexp.MustCompile(`\btar\s+(-[A-Za-z]+)\s+("[^"]+"|'[^']+'|\S+)`)
	// The command word of a checksum invocation, whose operands are the files
	// that reach checksums.txt.
	checksumCmdRe = regexp.MustCompile(`\b(shasum|sha256sum)\b`)
	// `echo "key=path" >> "$GITHUB_OUTPUT"`: one entry of the workflow's single
	// declaration of what it publishes.
	outputRe = regexp.MustCompile(`echo\s+"([A-Za-z0-9_]+)=([^"]+)"\s*>>\s*"\$GITHUB_OUTPUT"`)
	// `${{ steps.<id>.outputs.<key> }}`: a step reading a declared artifact back.
	stepOutputRe = regexp.MustCompile(`\$\{\{\s*steps\.[A-Za-z0-9_-]+\.outputs\.([A-Za-z0-9_]+)\s*\}\}`)
	// `$VAR` / `${VAR}` in a run block: an environment variable, which the
	// checksum step uses to receive a declared path.
	envVarRe = regexp.MustCompile(`^\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?$`)
	// `${version}`, `${VERSION}` and `$VERSION`: the workflow's one definition
	// of the version, resolved to a fixed string so a produced name and the
	// pattern that claims it can be compared as plain names. The braced-uppercase
	// form matters: the build steps use the lowercase `version` shell variable
	// while the artifact list and the checksum step use the uppercase `VERSION`
	// the workflow exports, and a produced name has to normalise to the same
	// placeholder as the declaration that claims it.
	versionVarRe = regexp.MustCompile(`\$\{?[Vv][Ee][Rr][Ss][Ii][Oo][Nn]\}?`)
)

// versionPlaceholder stands in for the release version. Every side of every
// comparison resolves it identically, so it cancels out and this test never has
// to know a real version.
const versionPlaceholder = "0.0.0-CHECKSUMINVARIANT"

// declaredArtifactsStep is the step that owns the artifact list. Both the
// checksum step and the upload steps are required to reach their paths through
// it, which is what makes it the single source of truth rather than one list
// among several.
const declaredArtifactsStep = "List the release artifacts"

type workflowStep struct {
	Name string            `yaml:"name"`
	ID   string            `yaml:"id"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
	With struct {
		Path string `yaml:"path"`
	} `yaml:"with"`
}

type releaseWorkflow struct {
	Jobs map[string]struct {
		Steps []workflowStep `yaml:"steps"`
	} `yaml:"jobs"`
}

func readReleaseWorkflow(t *testing.T, path string) releaseWorkflow {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // G304: the workflow this test checks
	if err != nil {
		t.Fatal(err)
	}

	var doc releaseWorkflow
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("release.yml is not valid YAML: %v", err)
	}

	return doc
}

func (d releaseWorkflow) steps() []workflowStep {
	var out []workflowStep

	for _, job := range d.Jobs {
		out = append(out, job.Steps...)
	}

	return out
}

func (d releaseWorkflow) step(name string) (workflowStep, bool) {
	for _, s := range d.steps() {
		if s.Name == name {
			return s, true
		}
	}

	return workflowStep{}, false
}

// artifactName is the name a user sees. Someone who downloads a release asset
// and runs `shasum -c checksums.txt` matches on the basename, and that is the
// identity compared here: the directory a file is staged in on the runner is
// an implementation detail, not part of what is published.
func artifactName(path string) string {
	return versionVarRe.ReplaceAllString(filepath.Base(strings.Trim(path, `"'`)), versionPlaceholder)
}

func sortedNames(names map[string]struct{}) []string {
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}

	sort.Strings(out)

	return out
}

// without is a sorted copy of names with one entry removed.
func without(names []string, drop string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		if name != drop {
			out = append(out, name)
		}
	}

	return out
}

// builtArchives is every archive the workflow's `tar` invocations create, in any
// job. An archive that is built but never declared is exactly the dead weight
// this test exists to catch, so the build side is measured from the tar
// commands themselves rather than from a list that could simply omit one.
//
// Only creation counts: the smoke job runs `tar -tzf` to assert the man page is
// packed at the right depth, and that lists a tarball nothing builds.
func builtArchives(doc releaseWorkflow) []string {
	names := map[string]struct{}{}

	for _, step := range doc.steps() {
		for _, m := range tarCreateRe.FindAllStringSubmatch(step.Run, -1) {
			flags := strings.TrimLeft(m[1], "-")
			if !strings.Contains(flags, "c") || !strings.Contains(flags, "f") {
				continue
			}

			names[artifactName(m[2])] = struct{}{}
		}
	}

	return sortedNames(names)
}

// declaredOutputNames is the workflow's one declaration of what it publishes:
// the artifact name the artifact-list step writes as each of its outputs,
// keyed by output name.
func declaredOutputNames(doc releaseWorkflow) map[string]string {
	step, ok := doc.step(declaredArtifactsStep)
	if !ok {
		return map[string]string{}
	}

	out := map[string]string{}
	for _, m := range outputRe.FindAllStringSubmatch(step.Run, -1) {
		out[m[1]] = artifactName(m[2])
	}

	return out
}

// sortedValues is a string map's values in a stable order, so a set comparison
// reports the same result on every run.
func sortedValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}

	sort.Strings(out)

	return out
}

// checksumOperands is the set of operands the workflow's checksum step feeds
// into checksums.txt: the arguments of the shasum/sha256sum invocation that
// writes it, with the options and the redirect target removed.
func checksumOperands(doc releaseWorkflow) []string {
	var out []string

	for _, step := range doc.steps() {
		for line := range strings.SplitSeq(step.Run, "\n") {
			loc := checksumCmdRe.FindStringIndex(line)
			if loc == nil {
				continue
			}

			// Only the invocation that WRITES the file is under test. The
			// publish job's `sha256sum` calls read single tarballs to render
			// the formula and never produce checksums.txt.
			if !strings.Contains(line, "checksums.txt") {
				continue
			}

			out = append(out, operands(line[loc[1]:])...)
		}
	}

	return out
}

func operands(command string) []string {
	var out []string

	fields := strings.Fields(command)
	for i := 0; i < len(fields); i++ {
		f := fields[i]

		// The redirect target is the file being written, not a file summed.
		if f == ">" || f == ">>" {
			break
		}

		if strings.HasPrefix(f, "-") {
			if shasumArgsWithValue[f] {
				i++
			}

			continue
		}

		out = append(out, strings.Trim(f, `"'`))
	}

	return out
}

// uploadPaths is what the release offers: the `path` of every
// actions/upload-artifact step. These are the artifacts the publish job
// downloads and hands to `gh release upload`, so they are the published set.
func uploadPaths(doc releaseWorkflow) []string {
	var out []string

	for _, step := range doc.steps() {
		if !strings.HasPrefix(step.Uses, "actions/upload-artifact@") {
			continue
		}

		for line := range strings.SplitSeq(step.With.Path, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}

			out = append(out, strings.TrimSpace(line))
		}
	}

	return out
}

// resolveReferences turns each reference a step makes into the artifact names
// that step actually acts on, so the checksummed set and the published set are
// each read off the workflow's own declaration instead of off a pattern that
// merely looks like it.
//
// Three forms are recognised, in the order a step would resolve them:
//   - `${{ steps.<id>.outputs.<key> }}`, read off the artifact-list step;
//   - `$VAR`, read through the step env that assigns it, which is how the
//     checksum step receives a declared path;
//   - a literal path or glob, taken at face value — except a glob, which is
//     resolved against the files the build creates, because a glob names a set
//     rather than a file.
//
// A reference to an output the list does not declare contributes nothing, which
// fails the equality in the test rather than matching nothing on both sides.
func resolveReferences(doc releaseWorkflow, refs, built []string) []string {
	// Every step's env is flattened together. GitHub scopes a step's env to that
	// step, and the names a checksum step uses are defined in its own `env:`
	// block, so a flat lookup finds each one where it is declared.
	env := map[string]string{}

	for _, step := range doc.steps() {
		maps.Copy(env, step.Env)
	}

	outputs := declaredOutputNames(doc)
	names := map[string]struct{}{}

	for _, ref := range refs {
		if m := envVarRe.FindStringSubmatch(ref); m != nil {
			ref = env[m[1]]
		}

		if m := stepOutputRe.FindStringSubmatch(ref); m != nil {
			if name, ok := outputs[m[1]]; ok {
				names[name] = struct{}{}
			}

			continue
		}

		name := artifactName(ref)

		// A literal name is taken as written. Intersecting it with the built set
		// here would be the wrong direction: an upload step naming a file the
		// build never creates is exactly the "uploaded but not checksummed" half
		// of the invariant, and filtering it away would hide it.
		if !isGlob(name) {
			names[name] = struct{}{}

			continue
		}

		for _, b := range built {
			// Patterns are artifact names already, so filepath.Match is used for
			// its own wildcard handling rather than a hand-rolled one.
			if ok, err := filepath.Match(name, b); err == nil && ok {
				names[b] = struct{}{}
			}
		}
	}

	return sortedNames(names)
}

func isGlob(name string) bool {
	return strings.ContainsAny(name, "*?[")
}
