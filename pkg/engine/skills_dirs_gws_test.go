package engine_test

import (
	"path/filepath"
	"testing"
)

// The skills directories a host reads. Tests that need to look at what a host
// really has on disk resolve them the same way the adapters do, so a rename of
// a host's layout shows up here first.
func claudeSkillsDir(home string) string {
	return filepath.Join(home, ".claude", "skills")
}

func openCodeSkillsDir(home string) string {
	return filepath.Join(home, ".config", "opencode", "skills")
}

func sharedSkillsDir(home string) string {
	return filepath.Join(home, ".agents", "skills")
}

func writeSkill(t *testing.T, dir, name, content string) {
	t.Helper()

	write(t, filepath.Join(dir, "skills", name, "SKILL.md"), content)
}

// a47CursorHome gives a fixture a cursor host with an empty MCP config, so a
// test that is about something else does not also trip the cursor host's
// "no mcp.json" check.
func a47CursorHome(t *testing.T, f *fixture) {
	t.Helper()

	write(t, filepath.Join(f.home, ".cursor", "mcp.json"), `{"mcpServers":{}}`)
}

// geminiHome gives a fixture a gemini host with an empty settings file, so a
// test that is about something else does not also trip the gemini host's
// "no settings.json" check.
func geminiHome(t *testing.T, f *fixture) {
	t.Helper()

	write(t, filepath.Join(f.home, ".gemini", "settings.json"), "{}")
}
