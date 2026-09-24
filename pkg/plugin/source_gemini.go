package plugin

import (
	"encoding/json"
	"path/filepath"
)

// geminiManifest is the gemini-extension.json shape.
type geminiManifest struct {
	Name        string                     `json:"name"`
	Version     string                     `json:"version"`
	Description string                     `json:"description"`
	MCPServers  map[string]json.RawMessage `json:"mcpServers"`
}

// readGemini reads the Gemini CLI extensions installed under
// <home>/.gemini/extensions. A directory without a gemini-extension.json is
// not an extension and is ignored silently.
func readGemini(home string) ([]Plugin, []string) {
	root := filepath.Join(home, geminiDir, extensionsDir)

	var (
		plugins []Plugin
		warns   []string
	)

	for _, name := range subdirs(root) {
		installPath := filepath.Join(root, name)

		var manifest geminiManifest

		state, manifestWarns := readJSONManifest(filepath.Join(installPath, extensionsFile), SourceGeminiCLI, &manifest)

		warns = append(warns, manifestWarns...)

		if state != manifestParsed {
			continue
		}

		plugin := Plugin{
			Source:      SourceGeminiCLI,
			Origin:      SourceGeminiCLI,
			Name:        fallback(manifest.Name, name),
			Version:     manifest.Version,
			Description: manifest.Description,
			Scope:       scopeUser,
			InstallPath: installPath,
		}

		plugin.fillArtifacts(SourceGeminiCLI, &warns)

		plugins = append(plugins, plugin)
	}

	return plugins, warns
}

// geminiArtifacts resolves a Gemini extension payload: skills/, agents/ and
// the TOML commands/ of the extension, hooks/hooks.json, and the inline
// mcpServers of the manifest.
func geminiArtifacts(installPath string) (pluginArtifacts, []string) {
	out := pluginArtifacts{
		Skills:   scanSkillDirs(filepath.Join(installPath, skillsDir)),
		Agents:   scanNamedFiles(filepath.Join(installPath, agentsDir), []string{markdownExt}, false),
		Commands: scanNamedFiles(filepath.Join(installPath, commandsDir), []string{".toml"}, false),
	}

	var warns []string

	hooks, hookWarns, err := ReadHooksFor(SourceGeminiCLI, installPath)
	if err != nil {
		warns = append(warns, warnf(SourceGeminiCLI, "plugin %s: %v", installPath, err))
	} else {
		warns = append(warns, hookWarns...)
	}

	mcp, mcpWarns := resolveMCPNames(SourceGeminiCLI, installPath)

	warns = append(warns, mcpWarns...)

	out.Hooks = hookEventNames(hooks)
	out.MCP = mcp

	return out, warns
}

// geminiMCPDocument synthesizes the MCP document of a Gemini extension from
// the inline mcpServers of its manifest.
func geminiMCPDocument(installPath string) ([]byte, string, []string, bool) {
	var manifest geminiManifest

	state, warns := readJSONManifest(filepath.Join(installPath, extensionsFile), SourceGeminiCLI, &manifest)

	if state != manifestParsed {
		return nil, "", warns, false
	}

	if len(manifest.MCPServers) == 0 {
		return nil, "", warns, false
	}

	data, err := json.Marshal(map[string]any{"mcpServers": manifest.MCPServers})
	if err != nil {
		return nil, extensionsFile, []string{warnf(SourceGeminiCLI, "plugin %s: cannot encode mcpServers: %v", installPath, err)}, false
	}

	return data, extensionsFile, warns, true
}
