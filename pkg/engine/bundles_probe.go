package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/hostcli"
	"github.com/odiumuniverse/beadle/pkg/state"
)

func probeFailure(err error, code int) string {
	if err != nil {
		return truncateNote(err.Error())
	}

	return fmt.Sprintf("exit status %d", code)
}

type claudePluginEntry struct {
	ID      string   `json:"id"`
	Version string   `json:"version"`
	Enabled bool     `json:"enabled"`
	Errors  []string `json:"errors"`
}

// probeBundle confirms that the host actually serves the freshly rendered
// bundle. It returns one of the three verification tiers and a note that is
// safe to store (never the raw listing, which embeds resolved secrets).
func (e *Engine) probeBundle(host bundle.Host, dir, version string) (string, string) {
	if e.binaryMissing(host) {
		return state.VerifyUnverifiable, host.Binary() + " CLI not found; the bundle stays unverified"
	}

	switch host {
	case bundle.Claude:
		return e.probeClaudeBundle(version)
	case bundle.Gemini:
		return e.probeGeminiBundle()
	default:
		return e.probeAntigravityBundle()
	}
}

// unreachableProbe maps a CLI that vanished before its probe ran to the tier
// a missing CLI gets: unverifiable, never failed.
func unreachableProbe(host bundle.Host, err error) (string, string, bool) {
	if !errors.Is(err, hostcli.ErrNotFound) {
		return "", "", false
	}

	return state.VerifyUnverifiable, host.Binary() + " CLI not found; the bundle stays unverified", true
}

func (e *Engine) probeClaudeBundle(version string) (string, string) {
	stdout, code, err := e.runHost(bundle.Claude, cliPlugin, "list", "--json")
	if tier, note, ok := unreachableProbe(bundle.Claude, err); ok {
		return tier, note
	}

	if err != nil || code != 0 {
		// The listing embeds resolved headers; only the stderr-bearing error
		// is safe to keep, never stdout.
		return state.VerifyFailed, "claude plugin list --json failed: " + probeFailure(err, code)
	}

	var entries []claudePluginEntry

	if err := json.Unmarshal(stdout, &entries); err != nil {
		return state.VerifyFailed, "claude plugin list --json output is not parseable"
	}

	id := bundle.PluginName + "@" + bundle.MarketplaceName

	for _, entry := range entries {
		if entry.ID != id {
			continue
		}

		switch {
		case !entry.Enabled:
			return state.VerifyFailed, "the host lists " + id + " as disabled"
		case len(entry.Errors) > 0:
			return state.VerifyFailed, truncateNote("the host reports plugin errors: " + strings.Join(entry.Errors, "; "))
		case entry.Version != version:
			return state.VerifyFailed, fmt.Sprintf("the host serves version %s while the bundle is %s", entry.Version, version)
		default:
			return state.VerifyExecuted, ""
		}
	}

	return state.VerifyFailed, "the host does not list " + id
}

func (e *Engine) probeGeminiBundle() (string, string) {
	stdout, code, err := e.runHost(bundle.Gemini, "extensions", "list", "--output-format", "json")
	if tier, note, ok := unreachableProbe(bundle.Gemini, err); ok {
		return tier, note
	}

	if err != nil || code != 0 {
		return state.VerifyFailed, "gemini extensions list failed: " + runOutput(err, stdout)
	}

	entries, ok := geminiExtensionEntries(stdout)
	if !ok {
		return state.VerifyFailed, truncateNote("gemini extensions list --output-format json is not parseable: " + strings.TrimSpace(string(stdout)))
	}

	for _, entry := range entries {
		if entry.name != bundle.PluginName {
			continue
		}

		if entry.errors != "" {
			return state.VerifyFailed, truncateNote("the host reports extension errors: " + entry.errors)
		}

		return state.VerifyExecuted, ""
	}

	return state.VerifyFailed, "the host does not list " + bundle.PluginName
}

type geminiExtension struct {
	name   string
	errors string
}

// geminiExtensionEntries reads the JSON listing tolerantly: the CLI emits an
// array of extensions, older builds may wrap it in an object.
func geminiExtensionEntries(stdout []byte) ([]geminiExtension, bool) {
	var raw []map[string]any

	if err := json.Unmarshal(stdout, &raw); err != nil {
		var wrapper struct {
			Extensions []map[string]any `json:"extensions"`
		}

		if err := json.Unmarshal(stdout, &wrapper); err != nil {
			return nil, false
		}

		raw = wrapper.Extensions
	}

	entries := make([]geminiExtension, 0, len(raw))

	for _, item := range raw {
		name, _ := item["name"].(string)

		entries = append(entries, geminiExtension{name: name, errors: nonEmptyField(item["errors"])})
	}

	return entries, true
}

func nonEmptyField(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []any:
		if len(typed) == 0 {
			return ""
		}

		data, err := json.Marshal(typed)
		if err != nil {
			return "unknown errors"
		}

		return string(data)
	default:
		data, err := json.Marshal(typed)
		if err != nil {
			return "unknown errors"
		}

		return string(data)
	}
}

// probeAntigravityBundle combines the host listing with a structural file
// check. The host is not launched, so the note says the check is structural.
func (e *Engine) probeAntigravityBundle() (string, string) {
	stdout, code, err := e.runHost(bundle.Antigravity, "plugin", "list")
	if tier, note, ok := unreachableProbe(bundle.Antigravity, err); ok {
		return tier, note
	}

	if err != nil || code != 0 {
		return state.VerifyFailed, "agy plugin list failed: " + runOutput(err, stdout)
	}

	if !strings.Contains(string(stdout), bundle.PluginName) {
		return state.VerifyFailed, truncateNote("agy plugin list does not mention beadle-canon: " + strings.TrimSpace(string(stdout)))
	}

	if note, ok := e.probeAntigravityFiles(); !ok {
		return state.VerifyFailed, note
	}

	return state.VerifyExecuted, "structural: agy plugin list plus the linked plugin files; the host was not launched"
}

func (e *Engine) probeAntigravityFiles() (string, bool) {
	var (
		linked bool
		warns  []string
	)

	for _, root := range e.antigravityLinks() {
		if !isDir(root) {
			continue
		}

		if note, ok := probeAntigravityRoot(root); !ok {
			warns = append(warns, note)
		} else {
			linked = true
		}
	}

	if !linked {
		if len(warns) > 0 {
			return truncateNote(strings.Join(warns, "; ")), false
		}

		return "the plugin is not linked into an antigravity customization root", false
	}

	return "", true
}

func probeAntigravityRoot(root string) (string, bool) {
	required := filepath.Join(root, "plugin.json")

	data, err := os.ReadFile(required) //nolint:gosec // G304: the path is under the caller-provided home
	if err != nil {
		return fmt.Sprintf("%s cannot be read: %v", required, err), false
	}

	if !json.Valid(data) {
		return required + " is not valid JSON", false
	}

	for _, name := range []string{"hooks.json", "mcp_config.json"} {
		path := filepath.Join(root, name)

		data, err := os.ReadFile(path) //nolint:gosec // G304: the path is under the caller-provided home
		switch {
		case err != nil && os.IsNotExist(err):
			continue
		case err != nil:
			return fmt.Sprintf("%s cannot be read: %v", path, err), false
		case !json.Valid(data):
			return path + " is not valid JSON", false
		}
	}

	return "", true
}
