package engine_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/fsutil"
)

const (
	agentWriterIterations = 4
	agentWriterPause      = 8 * time.Millisecond
	agentWriterAttempts   = 10
	agentWriterSettle     = time.Second
)

var errWriterStale = errors.New("writer state is stale")

func TestSyncKeepsConcurrentAgentWrites(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	f := newFixture(t)

	write(t, f.claudeConfig(), `{"mcpServers": {}, "agentBookkeeping":   {"kept":  true}}`)
	write(t, f.openCodeConfig(), `{"mcp": {"alpha": {"type": "local", "command": ["a"], "environment": {"KEY": "v1"}}}}`)
	write(t, f.claudeRules(), "# shared\n")
	write(t, f.openCodeRules(), "# shared\n")

	f.sync(t)

	claude := read(t, f.claudeConfig())
	require.Contains(t, claude, `"alpha"`)
	require.Contains(t, claude, `"agentBookkeeping":   {"kept":  true}`, "the untouched parts keep their bytes")

	write(t, f.openCodeConfig(), `{"mcp": {"alpha": {"type": "local", "command": ["a"], "environment": {"KEY": "v2"}}}}`)

	stop := make(chan struct{})

	var (
		stopOnce sync.Once
		wg       sync.WaitGroup
	)

	stopWriter := func() {
		stopOnce.Do(func() { close(stop) })

		wg.Wait()
	}

	t.Cleanup(stopWriter)

	wg.Go(func() {
		runConcurrentAgent(f, stop)
	})

	f.sync(t)

	stopWriter()

	f.sync(t)

	doc := readClaudeDoc(t, f.claudeConfig())
	env := nestedMap(t, doc, "mcpServers", "alpha", "env")
	require.Equal(t, "v2", env["KEY"], "the pulled change survives the race")
	require.Contains(t, doc, "writerCounter", "the concurrent agent key survives too")
	require.Contains(t, doc, "agentBookkeeping")
	require.Empty(t, claudeTempFiles(t, f.home), "no temp files are left behind")
}

func runConcurrentAgent(f *fixture, stop <-chan struct{}) {
	for i := range agentWriterIterations {
		select {
		case <-stop:
			return
		default:
		}

		if err := writeWriterCounter(f.claudeConfig(), i+1); err != nil {
			return
		}

		time.Sleep(agentWriterPause)
	}

	waitForAlphaKey(f.claudeConfig(), "v2", agentWriterSettle)

	_ = writeWriterCounter(f.claudeConfig(), agentWriterIterations+1)
}

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

func waitForAlphaKey(path, want string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if alphaKey(path) == want {
			return
		}

		time.Sleep(time.Millisecond)
	}
}

func alphaKey(path string) string {
	data, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
	if err != nil {
		return ""
	}

	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return ""
	}

	servers, ok := doc["mcpServers"].(map[string]any)
	if !ok {
		return ""
	}

	alpha, ok := servers["alpha"].(map[string]any)
	if !ok {
		return ""
	}

	env, ok := alpha["env"].(map[string]any)
	if !ok {
		return ""
	}

	key, _ := env["KEY"].(string)

	return key
}

func readClaudeDoc(t *testing.T, path string) map[string]any {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // G304: test reads its own temp file
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))

	return doc
}

func nestedMap(t *testing.T, doc map[string]any, keys ...string) map[string]any {
	t.Helper()

	current := doc

	for _, key := range keys {
		child, ok := current[key].(map[string]any)
		require.True(t, ok, "key %q must be an object", key)

		current = child
	}

	return current
}

func claudeTempFiles(t *testing.T, home string) []string {
	t.Helper()

	entries, err := os.ReadDir(home)
	require.NoError(t, err)

	var names []string

	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".claude.json.tmp-") {
			names = append(names, entry.Name())
		}
	}

	return names
}
