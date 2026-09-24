package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// HookHandler is one hook handler a plugin declares. The canon expresses a
// single shell command with a timeout, so the reader records every field the
// canon cannot carry in Unsupported instead of dropping it silently.
type HookHandler struct {
	Type    string
	Command string
	Timeout int
	// Unsupported lists the handler fields the canon cannot express, such as
	// `if`, a non-empty `args`, `async` or an unknown key. A non-empty list
	// makes the caller skip the handler with a warning.
	Unsupported []string
}

// HookGroup is one matcher group of a plugin hook event.
type HookGroup struct {
	Matcher string        `json:"matcher"`
	Hooks   []HookHandler `json:"hooks"`
}

// Hooks are the hook definitions of one plugin, keyed by the Claude event
// name (PreToolUse, SessionStart, …).
type Hooks map[string][]HookGroup

// ReadHooks reads the hook definitions of an installed plugin. It merges
// hooks/hooks.json with the inline manifest hooks; when both exist, the file
// wins with a warning, because the host treats the locations as alternatives.
func ReadHooks(installPath string) (Hooks, []string, error) {
	return ReadHooksFor(SourceClaudeCode, installPath)
}

// ReadHooksFor reads the hook definitions of one installed plugin in the
// document shape of its host: the Claude/Codex/Cursor wrapper, a bare event
// map (Gemini CLI), or the Antigravity owner-keyed document.
func ReadHooksFor(source, installPath string) (Hooks, []string, error) {
	if installPath == "" || !filepath.IsAbs(installPath) {
		return nil, []string{fmt.Sprintf("plugin install path %q is empty or not absolute; hooks not read", installPath)}, nil
	}

	switch source {
	case SourceCodex:
		return readCodexHooks(installPath)
	case SourceCursor:
		return readCursorHooks(installPath)
	case SourceGeminiCLI:
		return readHooksFile(filepath.Join(installPath, hooksDir, hooksFile))
	case SourceAntigravityCLI:
		return readAntigravityHooks(filepath.Join(installPath, hooksFile))
	default:
		return readClaudeHooks(installPath)
	}
}

// readClaudeHooks merges hooks/hooks.json with the inline hooks of the
// .claude-plugin/plugin.json manifest; the file wins when both exist.
func readClaudeHooks(installPath string) (Hooks, []string, error) {
	file, fileWarns, err := readHooksFile(filepath.Join(installPath, hooksDir, hooksFile))
	if err != nil {
		return nil, nil, err
	}

	inline, inlineWarns, err := readManifestHooks(installPath)
	if err != nil {
		return nil, nil, err
	}

	switch {
	case len(file) > 0 && len(inline) > 0:
		warns := slices.Clone(inlineWarns)
		warns = append(warns, "both hooks/hooks.json and inline manifest hooks are present; using hooks/hooks.json")

		return file, warns, nil
	case len(file) > 0:
		return file, fileWarns, nil
	default:
		return inline, append(fileWarns, inlineWarns...), nil
	}
}

// readCodexHooks reads the Codex hook locations: the explicit
// extensions.com.openai.hooks of the portable manifest wins, then the legacy
// .codex-plugin/plugin.json hooks, then the default hooks/hooks.json.
func readCodexHooks(installPath string) (Hooks, []string, error) {
	explicit, warns, err := codexManifestHooks(installPath)
	if err != nil || len(explicit) > 0 {
		return explicit, warns, err
	}

	// The default file is tried when the manifests declare no hooks: an
	// explicit value replaces default-file discovery.
	file, fileWarns, err := readHooksFile(filepath.Join(installPath, hooksDir, hooksFile))
	if err != nil {
		return nil, nil, err
	}

	return file, append(warns, fileWarns...), nil
}

// codexManifestHooks reads the explicit hook declaration of the Codex
// manifests: extensions.com.openai.hooks of the portable root manifest wins
// over the legacy .codex-plugin/plugin.json hooks field.
func codexManifestHooks(installPath string) (Hooks, []string, error) {
	raw, ok, err := codexPortableHooks(installPath)
	if err != nil {
		return nil, nil, err
	}

	if !ok {
		raw, ok, err = manifestHookField(filepath.Join(installPath, metaDir, metaFile))
		if err != nil {
			return nil, nil, err
		}

		if !ok {
			return nil, nil, nil
		}
	}

	return readHookSources(installPath, raw)
}

// codexPortableHooks reports the explicit extensions.com.openai.hooks of the
// portable root manifest.
func codexPortableHooks(installPath string) (json.RawMessage, bool, error) {
	path := filepath.Join(installPath, metaFile)

	data, err := os.ReadFile(path) //nolint:gosec // G304: the path is built from the caller-provided install path
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}

	var portable struct {
		Extensions map[string]json.RawMessage `json:"extensions"`
	}

	if err := json.Unmarshal(data, &portable); err != nil {
		return nil, false, fmt.Errorf("parse %s: %w", path, err)
	}

	raw, ok := portable.Extensions["com.openai"]
	if !ok {
		return nil, false, nil
	}

	var openai struct {
		Hooks json.RawMessage `json:"hooks"`
	}

	if err := json.Unmarshal(raw, &openai); err != nil {
		return nil, false, fmt.Errorf("parse %s: extensions.com.openai: %w", path, err)
	}

	return openai.Hooks, len(openai.Hooks) > 0, nil
}

// manifestHookField reads the raw `hooks` field of one manifest file; ok is
// false when the file is absent or declares no hooks.
func manifestHookField(path string) (json.RawMessage, bool, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the path is built from the caller-provided install path
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}

	var manifest struct {
		Hooks json.RawMessage `json:"hooks"`
	}

	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, false, fmt.Errorf("parse %s: %w", path, err)
	}

	return manifest.Hooks, len(manifest.Hooks) > 0, nil
}

// readCursorHooks reads the Cursor hook locations: the .cursor-plugin
// manifest hooks field wins, then the default hooks/hooks.json.
func readCursorHooks(installPath string) (Hooks, []string, error) {
	raw, ok, err := manifestHookField(filepath.Join(installPath, cursorMetaDir, metaFile))
	if err != nil {
		return nil, nil, err
	}

	if ok {
		return readHookSources(installPath, raw)
	}

	return readHooksFile(filepath.Join(installPath, hooksDir, hooksFile))
}

// readHookSources parses an explicit hooks declaration: a path (or a list of
// paths), an inline document (or a list of inline documents), or a mix. The
// documents merge; every path stays inside the plugin root.
func readHookSources(installPath string, raw json.RawMessage) (Hooks, []string, error) {
	out := Hooks{}

	var warns []string

	var asString string

	if err := json.Unmarshal(raw, &asString); err == nil {
		hooks, pathWarns, err := readHookPath(installPath, asString)
		if err != nil {
			return nil, nil, err
		}

		mergeHooks(out, hooks)

		return out, pathWarns, nil
	}

	var asList []json.RawMessage

	if err := json.Unmarshal(raw, &asList); err == nil {
		for _, item := range asList {
			var name string

			if err := json.Unmarshal(item, &name); err == nil {
				hooks, pathWarns, err := readHookPath(installPath, name)
				if err != nil {
					return nil, nil, err
				}

				warns = append(warns, pathWarns...)

				mergeHooks(out, hooks)

				continue
			}

			hooks, itemWarns, err := parseHookDocument(item)
			if err != nil {
				return nil, nil, err
			}

			warns = append(warns, itemWarns...)

			mergeHooks(out, hooks)
		}

		return out, warns, nil
	}

	hooks, docWarns, err := parseHookDocument(raw)
	if err != nil {
		return nil, nil, err
	}

	return hooks, docWarns, nil
}

func readHookPath(installPath, rel string) (Hooks, []string, error) {
	if !filepath.IsLocal(rel) {
		return nil, []string{fmt.Sprintf("plugin %s: hooks path %q escapes the plugin root; ignored", installPath, rel)}, nil
	}

	return readHooksFile(filepath.Join(installPath, filepath.FromSlash(strings.TrimPrefix(rel, "./"))))
}

func mergeHooks(into, from Hooks) {
	for event, groups := range from {
		into[event] = append(into[event], groups...)
	}
}

// readAntigravityHooks parses the owner-keyed Antigravity hooks.json:
// {"<owner>": {"enabled": true, "PreToolUse": [{type, command, timeout, matcher}], …}}.
func readAntigravityHooks(path string) (Hooks, []string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the path is built from the caller-provided install path
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}

	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", path, err)
	}

	var doc map[string]json.RawMessage

	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("%s: parse hooks: %w", path, err)
	}

	out := Hooks{}

	var warns []string

	for _, owner := range slices.Sorted(maps.Keys(doc)) {
		var groups map[string]json.RawMessage

		if err := json.Unmarshal(doc[owner], &groups); err != nil {
			warns = append(warns, fmt.Sprintf("hook owner %s is not an object; ignored", owner))

			continue
		}

		var enabled bool

		if raw, ok := groups["enabled"]; ok {
			if err := json.Unmarshal(raw, &enabled); err != nil {
				warns = append(warns, fmt.Sprintf("hook owner %s: enabled is not a boolean; ignored", owner))

				continue
			}
		} else {
			enabled = true
		}

		if !enabled {
			continue
		}

		for _, event := range slices.Sorted(maps.Keys(groups)) {
			if event == "enabled" {
				continue
			}

			var handlers []antigravityHandler

			if err := json.Unmarshal(groups[event], &handlers); err != nil {
				warns = append(warns, fmt.Sprintf("hook event %s of %s is not a list of handlers; ignored", event, owner))

				continue
			}

			for _, handler := range handlers {
				out[event] = append(out[event], HookGroup{Matcher: handler.Matcher, Hooks: []HookHandler{handler.hook()}})
			}
		}
	}

	return out, warns, nil
}

// antigravityHandler is one Antigravity hook handler: the matcher is part of
// the handler, unlike the Claude matcher group.
type antigravityHandler struct {
	Type    string
	Command string
	Timeout float64
	Matcher string
	Rest    map[string]json.RawMessage
}

func (h *antigravityHandler) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage

	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	if matcher, ok := raw["matcher"]; ok {
		if err := json.Unmarshal(matcher, &h.Matcher); err != nil {
			h.Matcher = ""
		}
	}

	delete(raw, "matcher")

	h.Rest = map[string]json.RawMessage{}

	for key, value := range raw {
		switch key {
		case "type":
			if err := json.Unmarshal(value, &h.Type); err != nil {
				h.Rest[key] = value
			}
		case "command":
			if err := json.Unmarshal(value, &h.Command); err != nil {
				h.Rest[key] = value
			}
		case "timeout":
			if err := json.Unmarshal(value, &h.Timeout); err != nil {
				h.Rest[key] = value
			}
		default:
			h.Rest[key] = value
		}
	}

	return nil
}

func (h antigravityHandler) hook() HookHandler {
	handler := HookHandler{Type: h.Type, Command: h.Command, Timeout: int(h.Timeout)}
	handler.Unsupported = slices.Sorted(maps.Keys(h.Rest))

	return handler
}

// readHooksFile parses a hooks document: either {"hooks": {…}} or the bare
// event map.
func readHooksFile(path string) (Hooks, []string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the path is built from the caller-provided install path
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}

	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", path, err)
	}

	hooks, warns, err := parseHookDocument(data)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}

	return hooks, warns, nil
}

// readManifestHooks parses the inline `hooks` field of the plugin manifest: an
// inline event map or a path to a hooks document. A path that escapes the
// plugin root is ignored with a warning.
func readManifestHooks(installPath string) (Hooks, []string, error) {
	path := filepath.Join(installPath, metaDir, metaFile)

	data, err := os.ReadFile(path) //nolint:gosec // G304: the path is built from the caller-provided install path
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}

	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", path, err)
	}

	var manifest struct {
		Hooks json.RawMessage `json:"hooks"`
	}

	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", path, err)
	}

	if len(manifest.Hooks) == 0 {
		return nil, nil, nil
	}

	var inline map[string]json.RawMessage

	if json.Unmarshal(manifest.Hooks, &inline) == nil {
		return parseHookDocument(manifest.Hooks)
	}

	var rel string

	if json.Unmarshal(manifest.Hooks, &rel) == nil {
		if !filepath.IsLocal(rel) {
			return nil, []string{fmt.Sprintf("plugin %s: manifest hooks path %q escapes the plugin root; ignored", installPath, rel)}, nil
		}

		return readHooksFile(filepath.Join(installPath, filepath.FromSlash(strings.TrimPrefix(rel, "./"))))
	}

	return nil, []string{fmt.Sprintf("plugin %s: the manifest hooks field is neither an inline map nor a path", installPath)}, nil
}

// parseHookDocument accepts the two documented document shapes: the
// hooks/hooks.json wrapper and a bare event map. Meta keys such as `$schema`
// and `description` are ignored; any other key whose value is not a list of
// matcher groups warns.
func parseHookDocument(data []byte) (Hooks, []string, error) {
	var raw map[string]json.RawMessage

	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("parse hooks: %w", err)
	}

	events := raw

	if wrapper, ok := raw["hooks"]; ok {
		events = map[string]json.RawMessage{}

		if err := json.Unmarshal(wrapper, &events); err != nil {
			return nil, nil, fmt.Errorf("parse hooks: %w", err)
		}
	}

	out := Hooks{}

	var warns []string

	for _, event := range slices.Sorted(maps.Keys(events)) {
		if metaHookKeys[event] {
			continue
		}

		var groups []HookGroup

		if err := json.Unmarshal(events[event], &groups); err != nil {
			warns = append(warns, fmt.Sprintf("hook event %s is not a list of matcher groups; ignored", event))

			continue
		}

		if len(groups) > 0 {
			out[event] = groups
		}
	}

	return out, warns, nil
}

// metaHookKeys are document keys that carry metadata rather than an event.
var metaHookKeys = map[string]bool{"$schema": true, "description": true}

// handlerKeys are the handler fields the reader understands. `shell` and
// `statusMessage` are cosmetic for a canon command: the host shell runs the
// command and the message only decorates the host UI. `args` is understood
// only when empty, because a canon command is a single shell string.
var handlerKeys = []string{"type", "command", "timeout", "shell", "statusMessage", "args"}

// UnmarshalJSON decodes one handler and records every field the canon cannot
// express.
func (h *HookHandler) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage

	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	for _, key := range slices.Sorted(maps.Keys(raw)) {
		if !slices.Contains(handlerKeys, key) {
			h.Unsupported = append(h.Unsupported, key)
		}
	}

	h.decodeType(raw)
	h.decodeCommand(raw)
	h.decodeTimeout(raw)
	h.decodeArgs(raw)

	return nil
}

func (h *HookHandler) decodeType(raw map[string]json.RawMessage) {
	value, ok := raw["type"]
	if !ok || json.Unmarshal(value, &h.Type) != nil {
		h.Unsupported = append(h.Unsupported, "type")
	}
}

func (h *HookHandler) decodeCommand(raw map[string]json.RawMessage) {
	value, ok := raw["command"]
	if !ok || json.Unmarshal(value, &h.Command) != nil {
		h.Unsupported = append(h.Unsupported, "command")
	}
}

func (h *HookHandler) decodeTimeout(raw map[string]json.RawMessage) {
	value, ok := raw["timeout"]
	if !ok {
		return
	}

	var timeout float64

	if err := json.Unmarshal(value, &timeout); err != nil || timeout < 0 || timeout > math.MaxInt32 {
		h.Unsupported = append(h.Unsupported, "timeout")

		return
	}

	h.Timeout = int(timeout)
}

func (h *HookHandler) decodeArgs(raw map[string]json.RawMessage) {
	value, ok := raw["args"]
	if !ok {
		return
	}

	var args []string

	if err := json.Unmarshal(value, &args); err != nil || len(args) > 0 {
		h.Unsupported = append(h.Unsupported, "args")
	}
}
