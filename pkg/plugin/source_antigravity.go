package plugin

import (
	"path/filepath"
)

// antigravityManifest is the plugin.json shape of an Antigravity plugin.
type antigravityManifest struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
}

// antigravityRoots lists the directories Antigravity loads plugins from, in
// install precedence order: the CLI data directory and the global config come
// before the shared .agents workspace.
func antigravityRoots(home string) []string {
	return []string{
		filepath.Join(home, geminiDir, antigravityDir, pluginsDir),
		filepath.Join(home, geminiDir, "config", pluginsDir),
		filepath.Join(home, agentsHomeDir, pluginsDir),
	}
}

// readAntigravity reads the Antigravity plugins of every install root. A
// directory without a plugin.json is not a plugin and is ignored silently;
// the first install root that provides a name wins, later copies warn.
func readAntigravity(home string) ([]Plugin, []string) {
	var (
		plugins []Plugin
		warns   []string
	)

	seen := map[string]string{}

	for _, root := range antigravityRoots(home) {
		for _, name := range subdirs(root) {
			installPath := filepath.Join(root, name)

			var manifest antigravityManifest

			state, manifestWarns := readJSONManifest(filepath.Join(installPath, metaFile), SourceAntigravityCLI, &manifest)

			warns = append(warns, manifestWarns...)

			if state != manifestParsed {
				continue
			}

			pluginName := fallback(manifest.Name, name)

			if first, duplicate := seen[pluginName]; duplicate {
				warns = append(warns, warnf(SourceAntigravityCLI, "plugin %s is installed in both %s and %s; using %s", pluginName, first, installPath, first))

				continue
			}

			seen[pluginName] = installPath

			plugin := Plugin{
				Source:      SourceAntigravityCLI,
				Origin:      SourceAntigravityCLI,
				Name:        pluginName,
				Version:     manifest.Version,
				Description: manifest.Description,
				Scope:       scopeUser,
				InstallPath: installPath,
			}

			plugin.fillArtifacts(SourceAntigravityCLI, &warns)

			plugins = append(plugins, plugin)
		}
	}

	return plugins, warns
}

// antigravityArtifacts resolves an Antigravity plugin payload: skills/,
// agents/, the owner-keyed hooks.json, and mcp_config.json. Rules/ carry no
// unified kind and are not presented.
func antigravityArtifacts(installPath string) (pluginArtifacts, []string) {
	out := pluginArtifacts{
		Skills: scanSkillDirs(filepath.Join(installPath, skillsDir)),
		Agents: scanNamedFiles(filepath.Join(installPath, agentsDir), []string{markdownExt}, false),
	}

	var warns []string

	hooks, hookWarns, err := ReadHooksFor(SourceAntigravityCLI, installPath)
	if err != nil {
		warns = append(warns, warnf(SourceAntigravityCLI, "plugin %s: %v", installPath, err))
	} else {
		warns = append(warns, hookWarns...)
	}

	mcp, mcpWarns := resolveMCPNames(SourceAntigravityCLI, installPath)

	warns = append(warns, mcpWarns...)

	out.Hooks = hookEventNames(hooks)
	out.MCP = mcp

	return out, warns
}
