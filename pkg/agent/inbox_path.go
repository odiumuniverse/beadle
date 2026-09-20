package agent

import (
	"os"
	"path/filepath"
)

// InboxPath returns the append-only inbox file of an agent, or "" when the
// agent has no inbox.
func InboxPath(home, id string) string {
	switch id {
	case OpenCodeID:
		dir := filepath.Join(home, ".config", "opencode")
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			dir = filepath.Join(xdg, "opencode")
		}

		return filepath.Join(dir, "inbox.md")
	case GeminiCLIID:
		return filepath.Join(home, ".gemini", "inbox.md")
	case CursorID:
		return filepath.Join(home, ".cursor", "inbox.md")
	case CodexID:
		return filepath.Join(home, ".codex", "inbox.md")
	case PiID:
		return filepath.Join(home, ".pi", "agent", "inbox.md")
	case KiloID:
		return filepath.Join(home, ".config", "kilo", "inbox.md")
	default:
		return ""
	}
}
