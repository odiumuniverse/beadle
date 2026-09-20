package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func withKey(t *testing.T, data []byte, key string) []byte {
	t.Helper()

	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	doc[key] = true

	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	return out
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}

	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("temp file %q left behind", entry.Name())
		}
	}
}

func TestUpdateFileRetriesAfterConcurrentWrite(t *testing.T) {
	Convey("Given a file that changes concurrently during the first build", t, func() {
		path := filepath.Join(t.TempDir(), "config.json")

		So(os.WriteFile(path, []byte(`{"a":1}`), 0o600), ShouldBeNil)

		calls := 0

		err := updateFile(path, 0o600, func(data []byte, present bool) ([]byte, bool, error) {
			calls++

			if !present {
				return nil, false, errors.New("expected the file to be present")
			}

			if calls == 1 {
				if writeErr := os.WriteFile(path, []byte(`{"a":1,"foreign":true}`), 0o600); writeErr != nil {
					return nil, false, writeErr
				}
			}

			if calls > 1 && !strings.Contains(string(data), `"foreign"`) {
				return nil, false, errors.New("the retry must build from the fresh content")
			}

			return withKey(t, data, "patch"), true, nil
		})

		Convey("When the update eventually succeeds", func() {
			got, readErr := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file

			Convey("Then both the foreign and the patched key survive", func() {
				So(err, ShouldBeNil)
				So(calls, ShouldBeGreaterThanOrEqualTo, 2)
				So(readErr, ShouldBeNil)
				So(string(got), ShouldContainSubstring, `"foreign"`)
				So(string(got), ShouldContainSubstring, `"patch"`)

				assertNoTempFiles(t, filepath.Dir(path))
			})
		})
	})
}

func TestUpdateFileGivesUpOnPersistentChange(t *testing.T) {
	Convey("Given a file that keeps changing on every attempt", t, func() {
		path := filepath.Join(t.TempDir(), "config.json")

		So(os.WriteFile(path, []byte(`{"writer":0}`), 0o600), ShouldBeNil)

		calls := 0

		err := updateFile(path, 0o600, func(_ []byte, _ bool) ([]byte, bool, error) {
			calls++

			if writeErr := os.WriteFile(path, []byte(fmt.Sprintf(`{"writer":%d}`, calls)), 0o600); writeErr != nil {
				return nil, false, writeErr
			}

			return []byte(`{"ours":true}`), true, nil
		})

		Convey("When the attempt budget runs out", func() {
			got, readErr := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file

			Convey("Then it gives up and leaves the writer's content", func() {
				So(err, ShouldBeError)
				So(errors.Is(err, errConcurrentWrite), ShouldBeTrue)
				So(err.Error(), ShouldContainSubstring, "after 5 attempts")
				So(calls, ShouldEqual, casAttempts)

				So(readErr, ShouldBeNil)
				So(string(got), ShouldEqual, fmt.Sprintf(`{"writer":%d}`, casAttempts))
				So(string(got), ShouldNotContainSubstring, "ours")

				assertNoTempFiles(t, filepath.Dir(path))
			})
		})
	})
}

func TestUpdateFileNoChangeSkipsWrite(t *testing.T) {
	Convey("Given a build that reports no change", t, func() {
		path := filepath.Join(t.TempDir(), "config.json")

		So(os.WriteFile(path, []byte(`{"a":1}`), 0o600), ShouldBeNil)

		err := updateFile(path, 0o600, func(_ []byte, present bool) ([]byte, bool, error) {
			if !present {
				return nil, false, errors.New("expected the file to be present")
			}

			if writeErr := os.WriteFile(path, []byte(`{"a":1,"foreign":true}`), 0o600); writeErr != nil {
				return nil, false, writeErr
			}

			return nil, false, nil
		})

		Convey("When the update runs", func() {
			got, readErr := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file

			Convey("Then the file is untouched", func() {
				So(err, ShouldBeNil)
				So(readErr, ShouldBeNil)
				So(string(got), ShouldEqual, `{"a":1,"foreign":true}`)
			})
		})
	})
}

func TestUpdateFileCreatesMissingFile(t *testing.T) {
	Convey("Given a missing file", t, func() {
		path := filepath.Join(t.TempDir(), "sub", "config.json")

		err := updateFile(path, 0o640, func(_ []byte, present bool) ([]byte, bool, error) {
			if present {
				return nil, false, errors.New("expected the file to be missing")
			}

			return []byte("created"), true, nil
		})

		Convey("When it is created", func() {
			got, readErr := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file

			So(err, ShouldBeNil)

			info, statErr := os.Stat(path)

			Convey("Then it exists with the default mode and a later no-op makes one call", func() {
				So(readErr, ShouldBeNil)
				So(string(got), ShouldEqual, "created")
				So(statErr, ShouldBeNil)
				So(info.Mode().Perm(), ShouldEqual, os.FileMode(0o640))

				calls := 0

				err := updateFile(path, 0o640, func(_ []byte, present bool) ([]byte, bool, error) {
					calls++

					if !present {
						return nil, false, errors.New("expected the file to be present")
					}

					return nil, false, nil
				})

				So(err, ShouldBeNil)
				So(calls, ShouldEqual, 1)
			})
		})
	})
}

func TestUpdateFileKeepsExistingPerm(t *testing.T) {
	Convey("Given a file with a non-default mode", t, func() {
		path := filepath.Join(t.TempDir(), "config.json")

		So(os.WriteFile(path, []byte(`{"a":1}`), 0o644), ShouldBeNil) //nolint:gosec // G306: the test needs a non-default mode to prove preservation

		err := updateFile(path, 0o600, func(_ []byte, present bool) ([]byte, bool, error) {
			if !present {
				return nil, false, errors.New("expected the file to be present")
			}

			return []byte(`{"a":2}`), true, nil
		})

		Convey("When it is updated", func() {
			info, statErr := os.Stat(path)

			Convey("Then the existing mode is preserved", func() {
				So(err, ShouldBeNil)
				So(statErr, ShouldBeNil)
				So(info.Mode().Perm(), ShouldEqual, os.FileMode(0o644))
			})
		})
	})
}
