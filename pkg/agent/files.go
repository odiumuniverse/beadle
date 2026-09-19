package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/tailscale/hujson"

	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

var errConcurrentWrite = errors.New("file changed concurrently")

const (
	casAttempts = 5
	casBackoff  = 20 * time.Millisecond
)

func readFile(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: agent config paths are resolved by the agent definitions
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}

	return data, true, nil
}

func anyExists(paths ...string) (bool, error) {
	for _, path := range paths {
		_, err := os.Stat(path) //nolint:gosec // G703: detection stats paths built by the agent definitions
		if err == nil {
			return true, nil
		}

		if !errors.Is(err, fs.ErrNotExist) {
			return false, fmt.Errorf("stat %s: %w", path, err)
		}
	}

	return false, nil
}

func writeFile(path string, data []byte, defaultPerm fs.FileMode) error {
	return writeFileChecked(path, data, defaultPerm, nil)
}

func writeFileChecked(path string, data []byte, defaultPerm fs.FileMode, check func() error) error {
	target, err := resolveLink(path)
	if err != nil {
		return err
	}

	return writeTarget(target, data, defaultPerm, check)
}

func writeTarget(target string, data []byte, defaultPerm fs.FileMode, check func() error) error {
	perm := defaultPerm

	info, err := os.Stat(target)

	switch {
	case err == nil:
		perm = info.Mode().Perm()
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("stat %s: %w", target, err)
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return fmt.Errorf("create directory for %s: %w", target, err)
	}

	return fsutil.WriteFileAtomicChecked(target, data, perm, check)
}

func updateFile(path string, defaultPerm fs.FileMode, build func(data []byte, present bool) ([]byte, bool, error)) error {
	return updateFileMode(path, defaultPerm, build, false)
}

func updateFileMode(path string, defaultPerm fs.FileMode, build func(data []byte, present bool) ([]byte, bool, error), hardened bool) error {
	for attempt := range casAttempts {
		if attempt > 0 {
			time.Sleep(casBackoff << (attempt - 1))
		}

		perm, err := writePerm(path, defaultPerm, hardened)
		if err != nil {
			return err
		}

		data, present, err := readFile(path)
		if err != nil {
			return err
		}

		out, changed, err := build(data, present)
		if err != nil {
			return err
		}

		if !changed {
			return nil
		}

		check := func() error {
			current, presentNow, err := readFile(path)
			if err != nil {
				return err
			}

			if presentNow != present || !bytes.Equal(current, data) {
				return errConcurrentWrite
			}

			return nil
		}

		if err := writeFileMode(path, out, perm, check, hardened); err != nil {
			if errors.Is(err, errConcurrentWrite) {
				continue
			}

			return err
		}

		return nil
	}

	return fmt.Errorf("%s: %w (after %d attempts)", path, errConcurrentWrite, casAttempts)
}

func writePerm(path string, defaultPerm fs.FileMode, hardened bool) (fs.FileMode, error) {
	if !hardened {
		return defaultPerm, nil
	}

	info, err := projectTargetInfo(path)
	if err != nil {
		return 0, err
	}

	if info != nil {
		return info.Mode().Perm(), nil
	}

	return defaultPerm, nil
}

func writeFileMode(path string, data []byte, perm fs.FileMode, check func() error, hardened bool) error {
	if !hardened {
		return writeFileChecked(path, data, perm, check)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create directory for %s: %w", path, err)
	}

	return fsutil.WriteFileAtomicChecked(path, data, perm, check)
}

func linkCount(info fs.FileInfo) uint64 {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 1
	}

	switch n := any(stat.Nlink).(type) {
	case uint64:
		return n
	case uint32:
		return uint64(n)
	case uint16:
		return uint64(n)
	}

	return 1
}

func resolveLink(path string) (string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return path, nil
	}

	if err != nil {
		return "", fmt.Errorf("stat %s: %w", path, err)
	}

	if info.Mode()&fs.ModeSymlink == 0 {
		return path, nil
	}

	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("broken symlink %s: %w", path, err)
	}

	return target, nil
}

func RealPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}

	return path
}

func decodePointer(data []byte, pointer string, dst any) (bool, error) {
	root, err := hujson.Parse(data)
	if err != nil {
		return false, fmt.Errorf("parse json: %w", err)
	}

	node := root.Find(pointer)
	if node == nil {
		return false, nil
	}

	standard, err := hujson.Standardize(node.Pack())
	if err != nil {
		return false, fmt.Errorf("standardize %s: %w", pointer, err)
	}

	if err := json.Unmarshal(standard, dst); err != nil {
		return false, fmt.Errorf("decode %s: %w", pointer, err)
	}

	return true, nil
}

func decodeObjects(data []byte, pointer string) (map[string]map[string]any, bool, error) {
	var raw map[string]any

	found, err := decodePointer(data, pointer, &raw)
	if err != nil || !found {
		return nil, found, err
	}

	out := make(map[string]map[string]any, len(raw))

	for name, value := range raw {
		if obj, ok := value.(map[string]any); ok {
			out[name] = obj
		}
	}

	return out, true, nil
}

type patchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

func addOp(path string, value any) patchOp {
	return patchOp{Op: "add", Path: path, Value: value}
}

func removeOp(path string) patchOp {
	return patchOp{Op: "remove", Path: path}
}

func applyPatch(data []byte, ops []patchOp) ([]byte, error) {
	root, err := hujson.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}

	patch, err := json.Marshal(ops)
	if err != nil {
		return nil, fmt.Errorf("encode patch: %w", err)
	}

	if err := root.Patch(patch); err != nil {
		return nil, fmt.Errorf("apply patch: %w", err)
	}

	return root.Pack(), nil
}

func pointerJoin(pointer, token string) string {
	return pointer + "/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(token)
}

func jsonEqual(a, b any) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}

	right, err := json.Marshal(b)
	if err != nil {
		return false
	}

	leftCanonical, err := kind.CanonicalJSON(left)
	if err != nil {
		return false
	}

	rightCanonical, err := kind.CanonicalJSON(right)
	if err != nil {
		return false
	}

	return bytes.Equal(leftCanonical, rightCanonical)
}

func roundTrip(value map[string]any) (map[string]any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode entry: %w", err)
	}

	out := map[string]any{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode entry: %w", err)
	}

	return out, nil
}

func toStringSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}

	out := make([]string, 0, len(items))

	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}

	return out
}

func toStringMap(value any) map[string]string {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil
	}

	out := make(map[string]string, len(obj))

	for key, item := range obj {
		if s, ok := item.(string); ok {
			out[key] = s
		}
	}

	return out
}

func stringField(entry map[string]any, key string) string {
	value, _ := entry[key].(string)

	return value
}
