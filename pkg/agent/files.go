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

	added := addedOps(root, ops)
	inline := inlineParents(root, added)

	if err := root.Patch(patch); err != nil {
		return nil, fmt.Errorf("apply patch: %w", err)
	}

	formatPatchedValues(&root, added, inline, detectIndent(data))

	return root.Pack(), nil
}

func addedOps(root hujson.Value, ops []patchOp) []patchOp {
	var added []patchOp

	for _, op := range ops {
		if op.Op == "add" && root.Find(op.Path) == nil {
			added = append(added, op)
		}
	}

	return added
}

func inlineParents(root hujson.Value, ops []patchOp) map[string]bool {
	inline := map[string]bool{}

	for _, op := range ops {
		parent, _, ok := splitPointer(op.Path)
		if !ok {
			continue
		}

		if _, seen := inline[parent]; seen {
			continue
		}

		node := root.Find(parent)
		if node == nil {
			inline[parent] = false

			continue
		}

		if obj, ok := node.Value.(*hujson.Object); ok {
			inline[parent] = objectInline(obj)
		}
	}

	return inline
}

func objectInline(obj *hujson.Object) bool {
	if len(obj.Members) == 0 {
		return false
	}

	for i := range obj.Members {
		if _, ok := trailingWhitespace(obj.Members[i].Name.BeforeExtra); ok {
			return false
		}
	}

	return true
}

func formatPatchedValues(root *hujson.Value, ops []patchOp, inline map[string]bool, unit string) {
	if unit == "" {
		return
	}

	for _, op := range ops {
		formatPatchedValue(root, op, inline, unit)
	}
}

func formatPatchedValue(root *hujson.Value, op patchOp, inline map[string]bool, unit string) {
	parent, name, ok := splitPointer(op.Path)
	if !ok || inline[parent] {
		return
	}

	node := root.Find(parent)
	if node == nil {
		return
	}

	obj, ok := node.Value.(*hujson.Object)
	if !ok {
		return
	}

	member := objectMember(obj, name)
	if member == nil {
		return
	}

	formatMember(obj, member, op.Value, unit, pointerDepth(op.Path))
}

func formatMember(obj *hujson.Object, member *hujson.ObjectMember, value any, unit string, depth int) {
	indent, after, ok := memberIndent(obj, member, depth, unit)
	if !ok || (after != nil && !isWhitespace(obj.AfterExtra)) {
		return
	}

	fragment, err := prettyValue(value, indent, unit)
	if err != nil {
		return
	}

	parsed, err := hujson.Parse(fragment)
	if err != nil {
		return
	}

	extra := member.Name.BeforeExtra

	switch {
	case len(extra) == 0:
		member.Name.BeforeExtra = hujson.Extra("\n" + indent)
	case bytes.HasSuffix(extra, []byte("\n")):
		member.Name.BeforeExtra = hujson.Extra(string(extra) + indent)
	case trailingComment(extra):
		member.Name.BeforeExtra = hujson.Extra(string(bytes.TrimRight(extra, " \t")) + "\n" + indent)
	}

	if len(member.Value.BeforeExtra) == 0 {
		if extra := colonWhitespace(obj, member); extra != nil {
			member.Value.BeforeExtra = extra
		} else {
			member.Value.BeforeExtra = hujson.Extra(" ")
		}
	}

	if after != nil {
		obj.AfterExtra = hujson.Extra(*after)
	}

	member.Value.Value = parsed.Value
}

func trailingComment(extra hujson.Extra) bool {
	idx := bytes.LastIndexByte(extra, '\n')

	return bytes.ContainsRune(extra[idx+1:], '/')
}

func colonWhitespace(obj *hujson.Object, member *hujson.ObjectMember) hujson.Extra {
	for i := range obj.Members {
		if &obj.Members[i] == member {
			continue
		}

		if extra := obj.Members[i].Value.BeforeExtra; len(extra) > 0 && isWhitespace(extra) {
			return extra
		}
	}

	return nil
}

func isWhitespace(extra hujson.Extra) bool {
	for _, b := range extra {
		if b != ' ' && b != '\t' {
			return false
		}
	}

	return true
}

func splitPointer(path string) (string, string, bool) {
	idx := strings.LastIndexByte(path, '/')
	if idx < 0 {
		return "", "", false
	}

	token := strings.NewReplacer("~1", "/", "~0", "~").Replace(path[idx+1:])

	return path[:idx], token, true
}

func pointerDepth(path string) int {
	return strings.Count(path, "/")
}

func objectMember(obj *hujson.Object, name string) *hujson.ObjectMember {
	for i := len(obj.Members) - 1; i >= 0; i-- {
		literal, ok := obj.Members[i].Name.Value.(hujson.Literal)
		if ok && literal.String() == name {
			return &obj.Members[i]
		}
	}

	return nil
}

func memberIndent(obj *hujson.Object, member *hujson.ObjectMember, depth int, unit string) (string, *string, bool) {
	if indent, ok := trailingWhitespace(member.Name.BeforeExtra); ok && indent != "" {
		return indent, nil, true
	}

	for i := range obj.Members {
		if &obj.Members[i] == member {
			continue
		}

		if indent, ok := trailingWhitespace(obj.Members[i].Name.BeforeExtra); ok {
			return indent, nil, true
		}
	}

	if indent, ok := trailingWhitespace(obj.AfterExtra); ok {
		return indent + unit, nil, true
	}

	after := "\n" + strings.Repeat(unit, max(depth-1, 0))

	return strings.Repeat(unit, depth), &after, true
}

func trailingWhitespace(extra hujson.Extra) (string, bool) {
	idx := bytes.LastIndexByte(extra, '\n')
	if idx < 0 {
		return "", false
	}

	suffix := string(extra[idx+1:])
	if !isWhitespace(hujson.Extra(suffix)) {
		return "", false
	}

	return suffix, true
}

func prettyValue(value any, indent, unit string) ([]byte, error) {
	rendered, err := json.MarshalIndent(value, "", unit)
	if err != nil {
		return nil, fmt.Errorf("encode value: %w", err)
	}

	return []byte(strings.ReplaceAll(string(rendered), "\n", "\n"+indent)), nil
}

func detectIndent(data []byte) string {
	indent := ""
	structural := false

	for line := range strings.SplitSeq(string(data), "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" || trimmed == line {
			continue
		}

		switch trimmed[0] {
		case '"', '}', ']':
		default:
			continue
		}

		structural = true

		prefix := line[:len(line)-len(trimmed)]
		if prefix == "" {
			continue
		}

		if indent == "" || len(prefix) < len(indent) {
			indent = prefix
		}
	}

	if !structural {
		return ""
	}

	return indent
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
