package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
)

const MaxTimeout = 600

// SourcePluginPrefix marks a canon hook approved from an installed plugin;
// the rest of the value is the plugin key (<marketplace>/<name>).
const SourcePluginPrefix = "plugin:"

type Hook struct {
	Event   string `json:"event"`
	Matcher string `json:"matcher,omitempty"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
	// Source records where the hook came from; empty for authored hooks and
	// "plugin:<marketplace>/<name>" for hooks approved from a plugin.
	Source string `json:"source,omitempty"`
}

// PluginKey returns the plugin key of a plugin-sourced hook and whether the
// hook carries one.
func (h Hook) PluginKey() (string, bool) {
	key, ok := strings.CutPrefix(h.Source, SourcePluginPrefix)
	if !ok || key == "" {
		return "", false
	}

	return key, true
}

// Canon event names; host renderers map them to the host vocabulary.
const (
	EventNotification = "notification"
	EventPostTool     = "post-tool"
	EventPreTool      = "pre-tool"
	EventSessionStart = "session-start"
	EventStop         = "stop"
)

var (
	namePattern = regexp.MustCompile(`^[a-z0-9-]+$`)
	events      = []string{EventNotification, EventPostTool, EventPreTool, EventSessionStart, EventStop}
)

func Events() []string {
	return slices.Clone(events)
}

func ValidEvent(event string) bool {
	return slices.Contains(events, event)
}

func ValidName(name string) bool {
	return namePattern.MatchString(name)
}

func Validate(name string, hook Hook) error {
	if !ValidName(name) {
		return fmt.Errorf("invalid hook name %q (expected [a-z0-9-]+)", name)
	}

	if !ValidEvent(hook.Event) {
		return fmt.Errorf("hook %s: unknown event %q (expected %s)", name, hook.Event, strings.Join(events, ", "))
	}

	if strings.TrimSpace(hook.Command) == "" {
		return fmt.Errorf("hook %s: command is required", name)
	}

	if hook.Timeout < 0 || hook.Timeout > MaxTimeout {
		return fmt.Errorf("hook %s: timeout %d is out of range 0..%d", name, hook.Timeout, MaxTimeout)
	}

	return nil
}

func Load(path string) (map[string]Hook, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is the vault hooks canon
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]Hook{}, nil
	}

	if err != nil {
		return nil, fmt.Errorf("read hooks: %w", err)
	}

	doc := map[string]Hook{}

	if len(bytes.TrimSpace(data)) == 0 {
		return doc, nil
	}

	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse hooks: %w", err)
	}

	return doc, nil
}

func Save(path string, hooks map[string]Hook) error {
	data, err := json.MarshalIndent(hooks, "", "  ")
	if err != nil {
		return fmt.Errorf("encode hooks: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create hooks directory: %w", err)
	}

	if err := fsutil.WriteFileAtomic(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write hooks: %w", err)
	}

	return nil
}

func Approved(cfg *config.Config) map[string]bool {
	out := map[string]bool{}

	if cfg == nil {
		return out
	}

	for _, name := range cfg.ApprovedHooks {
		out[name] = true
	}

	return out
}
