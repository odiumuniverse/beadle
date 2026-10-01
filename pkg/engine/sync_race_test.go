package engine_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
)

// agentWriterAttempts bounds the fake agent's own retry on a lost race, so it
// cannot spin forever against a writer that keeps winning.
const agentWriterAttempts = 10

var errWriterStale = errors.New("writer state is stale")

// A host agent editing its own config while sync runs loses nothing — not the
// agent's bytes, not sync's, and nothing temporary left behind.
//
// This test used to chase the collision with a clock: it slept between the
// agent's writes, then waited for sync to deliver v2 under a one-second budget
// whose result it discarded, so a timeout was indistinguishable from success.
// Under -race with four packages competing a single sync can outlast that
// budget, and the test then failed with "Expected v2, Actual v1" for a run that
// had never been given the chance to write v2 at all. run5 caught it; fifty
// clean runs on an idle machine could not, which is the signature of a test that
// is measuring the scheduler.
//
// Removing the sleeps did not fix it, it moved it. An agent that writes as fast
// as it can starves the very CAS loop that protects it: sync loses five times in
// a row and refuses with "file changed concurrently (after 5 attempts)", so the
// test traded one flake for another.
//
// So the collision is now a fact rather than a probability. agent.BeforeGuarded-
// Write fires inside the CAS window — after the base read, before the guarded
// write — which is the only place a concurrent writer can win, and it is not
// reachable from a test without help. The fake agent performs its write there,
// once, on demand. Sync's guard then finds the file has moved, retries, re-reads,
// and merges: the interleaving that used to be hoped for is now the one the test
// asked for, and it happens on the first attempt of every run.
func TestSyncKeepsConcurrentAgentWrites(t *testing.T) {
	Convey("Given a fixture where a host agent writes during sync", t, func() {
		t.Setenv("XDG_CONFIG_HOME", "")

		f := newFixture(t)

		write(t, f.claudeConfig(), `{"mcpServers": {}, "agentBookkeeping":   {"kept":  true}}`)
		write(t, f.openCodeConfig(), `{"mcp": {"alpha": {"type": "local", "command": ["a"], "environment": {"KEY": "v1"}}}}`)
		write(t, f.claudeRules(), "# shared\n")
		write(t, f.openCodeRules(), "# shared\n")

		f.sync(t)

		// The agent's change, in the opencode host. Carrying it to claude is
		// sync's half of the race, and KEY is the value that used to go missing.
		write(t, f.openCodeConfig(), `{"mcp": {"alpha": {"type": "local", "command": ["a"], "environment": {"KEY": "v2"}}}}`)

		Convey("Then the agent's write lands inside that window and both survive", func() {
			var (
				once  sync.Once
				fired bool
			)

			restore := func(path string) {
				if path != f.claudeConfig() {
					return
				}

				once.Do(func() {
					// Exactly one interleaving per run. Every extra collision
					// would make the test a race again, and this one is already
					// the worst case sync has to survive.
					if err := writeWriterCounter(path, 1); err == nil {
						fired = true
					}
				})
			}

			prev := agent.BeforeGuardedWrite
			agent.BeforeGuardedWrite = restore

			t.Cleanup(func() { agent.BeforeGuardedWrite = prev })

			f.sync(t)

			Convey("And the window was really entered", func() {
				// Without this the test passes even if the seam is never called,
				// which is the failure mode a test written around a hook is most
				// prone to: the hook moves, the assertions stay, the proof is gone.
				So(fired, ShouldBeTrue)
			})

			doc := readClaudeDoc(t, f.claudeConfig())
			env := nestedMap(t, doc, "mcpServers", "alpha", "env")

			Convey("And sync's change survived the agent's", func() {
				So(env["KEY"], ShouldEqual, "v2")
			})

			Convey("And the agent's change survived sync's", func() {
				// If sync ever wrote from a stale read without comparing first,
				// this key is what disappears.
				So(doc, ShouldContainKey, "writerCounter")
			})

			Convey("And neither writer dropped a key it did not come for", func() {
				So(doc, ShouldContainKey, "agentBookkeeping")
			})

			Convey("And no temporary file outlived either of them", func() {
				So(claudeTempFiles(t, f.home), ShouldBeEmpty)
			})
		})
	})
}

// writeWriterCounter is the fake agent's whole job: read its own document, add a
// key of its own, and write it back. Its own CAS is what makes the agent a fair
// opponent — an agent that clobbered blindly would be testing the wrong thing.
func writeWriterCounter(path string, counter int) error {
	for range agentWriterAttempts {
		err := tryWriteWriterCounter(path, counter)
		if err == nil {
			return nil
		}

		if !errors.Is(err, errWriterStale) {
			return err
		}
	}

	return errWriterStale
}

func tryWriteWriterCounter(path string, counter int) error {
	data, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
	if err != nil {
		return err
	}

	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return err
	}

	doc["writerCounter"] = counter

	out, err := json.Marshal(doc)
	if err != nil {
		return err
	}

	return fsutil.WriteFileAtomicChecked(path, out, 0o600, func() error {
		current, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
		if err != nil {
			return err
		}

		if !bytes.Equal(current, data) {
			return errWriterStale
		}

		return nil
	})
}

func readClaudeDoc(t *testing.T, path string) map[string]any {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}

	return doc
}

func nestedMap(t *testing.T, doc map[string]any, keys ...string) map[string]any {
	t.Helper()

	current := doc

	for _, key := range keys {
		child, ok := current[key].(map[string]any)
		if !ok {
			t.Fatalf("key %q must be an object", key)
		}

		current = child
	}

	return current
}

func claudeTempFiles(t *testing.T, home string) []string {
	t.Helper()

	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatalf("readdir %s: %v", home, err)
	}

	var names []string

	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".claude.json.tmp-") {
			names = append(names, entry.Name())
		}
	}

	return names
}
