package hostcli_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/hostcli"
)

// script writes an executable shell script and returns its path.
func script(t *testing.T, dir, name, body string, perm os.FileMode) string {
	t.Helper()

	path := filepath.Join(dir, name)

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}

	if err := os.Chmod(path, perm); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}

	return path
}

func missingOnPATH(name string) (string, error) {
	return "", errors.New(name + ": not found")
}

func TestResolve(t *testing.T) {
	Convey("Given a CLI on the process PATH", t, func() {
		r := hostcli.NewResolver(hostcli.WithLookPath(func(name string) (string, error) { return "/usr/bin/" + name, nil }))

		Convey("When it resolves", func() {
			bin, err := r.Resolve("claude")

			Convey("Then the PATH hit wins and keeps the process environment", func() {
				So(err, ShouldBeNil)
				So(bin, ShouldResemble, hostcli.Binary{Name: "claude", Path: "/usr/bin/claude", Source: hostcli.SourceLookPath})
			})
		})
	})

	Convey("Given a CLI missing from the PATH and never recorded", t, func() {
		r := hostcli.NewResolver(hostcli.WithLookPath(missingOnPATH))

		Convey("When it resolves", func() {
			_, err := r.Resolve("claude")

			Convey("Then it is not found", func() {
				So(errors.Is(err, hostcli.ErrNotFound), ShouldBeTrue)
			})
		})
	})

	Convey("Given a CLI missing from the PATH but recorded by an attended run", t, func() {
		dir := t.TempDir()
		path := script(t, dir, "claude", "#!/bin/sh\necho ok\n", 0o755)

		r := hostcli.NewResolver(
			hostcli.WithLookPath(missingOnPATH),
			hostcli.WithRecords(hostcli.Records{"claude": {Path: path, PATH: "/opt/tools/bin:/usr/bin"}}),
		)

		Convey("When it resolves", func() {
			bin, err := r.Resolve("claude")

			Convey("Then the record answers and carries its PATH", func() {
				So(err, ShouldBeNil)
				So(bin, ShouldResemble, hostcli.Binary{Name: "claude", Path: path, Source: hostcli.SourceRecorded, PATH: "/opt/tools/bin:/usr/bin"})
			})
		})
	})
}

func TestCheck(t *testing.T) {
	Convey("Given recorded locations that no longer hold the CLI", t, func() {
		dir := t.TempDir()

		cases := map[string]hostcli.Record{
			"relative":       {Path: "bin/claude"},
			"wrong name":     {Path: script(t, dir, "gemini", "#!/bin/sh\n", 0o755)},
			"not executable": {Path: script(t, t.TempDir(), "claude", "#!/bin/sh\n", 0o644)},
			"other-writable": {Path: script(t, t.TempDir(), "claude", "#!/bin/sh\n", 0o757)},
			"gone":           {Path: filepath.Join(dir, "missing", "claude")},
		}

		for label, record := range cases {
			Convey("When the record is "+label, func() {
				_, err := hostcli.NewResolver(
					hostcli.WithLookPath(missingOnPATH),
					hostcli.WithRecords(hostcli.Records{"claude": record}),
				).Resolve("claude")

				Convey("Then the record is rejected as not found", func() {
					So(errors.Is(err, hostcli.ErrNotFound), ShouldBeTrue)
				})
			})
		}
	})

	Convey("Given a recorded env-shebang script", t, func() {
		bin := t.TempDir()
		path := script(t, t.TempDir(), "gemini", "#!/usr/bin/env -S fakenode --flag\nconsole.log(1)\n", 0o755)

		Convey("When the recorded PATH does not reach its interpreter", func() {
			err := hostcli.Check("gemini", hostcli.Record{Path: path, PATH: "/nowhere"})

			Convey("Then the record is unusable", func() {
				So(err, ShouldNotBeNil)
			})
		})

		Convey("When the recorded PATH reaches its interpreter", func() {
			script(t, bin, "fakenode", "#!/bin/sh\n", 0o755)

			err := hostcli.Check("gemini", hostcli.Record{Path: path, PATH: "/nowhere:" + bin})

			Convey("Then the record is usable", func() {
				So(err, ShouldBeNil)
			})
		})
	})
}

func TestBinaryRun(t *testing.T) {
	Convey("Given a recorded binary", t, func() {
		dir := t.TempDir()
		path := script(t, dir, "claude", "#!/bin/sh\n"+
			"if [ \"$1\" = fail ]; then echo boom >&2; exit 3; fi\n"+
			"if [ \"$1\" = --version ]; then printf '2.1.281 (Claude Code)\\nmore\\n'; exit 0; fi\n"+
			"echo \"$PATH|$*\"\n", 0o755)

		bin := hostcli.Binary{Name: "claude", Path: path, Source: hostcli.SourceRecorded, PATH: "/recorded/bin:/usr/bin:/bin"}

		Convey("When it runs", func() {
			out, err := bin.Run(t.Context(), []string{"plugin", "list"}, nil)

			Convey("Then it runs with the recorded PATH", func() {
				So(err, ShouldBeNil)
				So(string(out), ShouldEqual, "/recorded/bin:/usr/bin:/bin|plugin list\n")
			})
		})

		Convey("When the host rejects the call", func() {
			_, err := bin.Run(t.Context(), []string{"fail"}, nil)

			exitErr, ok := errors.AsType[*hostcli.ExitError](err)

			Convey("Then it is an exit error with the code and stderr, not a missing CLI", func() {
				So(ok, ShouldBeTrue)
				So(exitErr.Code, ShouldEqual, 3)
				So(exitErr.Stderr, ShouldEqual, "boom")
				So(errors.Is(err, hostcli.ErrNotFound), ShouldBeFalse)
			})
		})

		Convey("When the binary vanished after it resolved", func() {
			So(os.Remove(path), ShouldBeNil)

			_, err := bin.Run(t.Context(), nil, nil)

			Convey("Then it is not found, never a host failure", func() {
				So(errors.Is(err, hostcli.ErrNotFound), ShouldBeTrue)
			})
		})

		Convey("When its version is asked", func() {
			version, err := bin.Version(t.Context())

			Convey("Then the first line comes back", func() {
				So(err, ShouldBeNil)
				So(version, ShouldEqual, "2.1.281 (Claude Code)")
			})
		})
	})
}

func TestRecords(t *testing.T) {
	Convey("Given a records file path", t, func() {
		path := filepath.Join(t.TempDir(), "state", "hostcli.json")

		Convey("When nothing was recorded yet", func() {
			records, err := hostcli.LoadRecords(path)

			Convey("Then it loads empty", func() {
				So(err, ShouldBeNil)
				So(records, ShouldBeEmpty)
			})
		})

		Convey("When records are saved and loaded", func() {
			at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
			saved := hostcli.Records{"claude": {Path: "/Users/u/.local/bin/claude", PATH: "/Users/u/.local/bin:/usr/bin", At: at}}

			So(saved.Save(path), ShouldBeNil)

			loaded, err := hostcli.LoadRecords(path)
			So(err, ShouldBeNil)

			info, err := os.Stat(path)
			So(err, ShouldBeNil)

			Convey("Then they round-trip and stay private to the owner", func() {
				So(loaded, ShouldResemble, saved)
				So(info.Mode().Perm(), ShouldEqual, os.FileMode(0o600))
			})
		})
	})
}
