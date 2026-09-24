package plugin

import (
	"encoding/json"
	"path/filepath"
)

// cursorManifest is the union of the two Cursor plugin manifests: the
// .cursor-plugin/plugin.json of a Cursor Plugin and the portable root
// plugin.json of an Agent Plugins package.
type cursorManifest struct {
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Description string          `json:"description"`
	Skills      stringList      `json:"skills"`
	Agents      stringList      `json:"agents"`
	Commands    stringList      `json:"commands"`
	Hooks       json.RawMessage `json:"hooks"`
	MCPServers  json.RawMessage `json:"mcpServers"`
}

// cursorRoot returns the local plugin directory of Cursor.
func cursorRoot(home string) string {
	return filepath.Join(home, ".cursor", pluginsDir, localPluginsDir)
}

// readCursor reads the Cursor local plugins (~/.cursor/plugins/local). A
// directory without a manifest is not a plugin and is ignored silently.
func readCursor(home string) ([]Plugin, []string) {
	var (
		plugins []Plugin
		warns   []string
	)

	for _, name := range subdirs(cursorRoot(home)) {
		installPath := filepath.Join(cursorRoot(home), name)

		manifest, state, manifestWarns := readCursorManifest(installPath)

		warns = append(warns, manifestWarns...)

		if state != manifestParsed {
			continue
		}

		plugin := Plugin{
			Source:      SourceCursor,
			Origin:      SourceCursor,
			Name:        fallback(manifest.Name, name),
			Version:     manifest.Version,
			Description: manifest.Description,
			Scope:       scopeUser,
			InstallPath: installPath,
		}

		plugin.fillArtifacts(SourceCursor, &warns)

		plugins = append(plugins, plugin)
	}

	return plugins, warns
}

// readCursorManifest reads the Cursor manifest candidates: the
// .cursor-plugin/plugin.json of a Cursor Plugin wins over the portable root
// plugin.json of an Agent Plugins package.
func readCursorManifest(dir string) (cursorManifest, manifestState, []string) {
	candidates := []string{
		filepath.Join(dir, cursorMetaDir, metaFile),
		filepath.Join(dir, metaFile),
	}

	for _, path := range candidates {
		var manifest cursorManifest

		state, warns := readJSONManifest(path, SourceCursor, &manifest)
		if state == manifestMissing {
			continue
		}

		return manifest, state, warns
	}

	return cursorManifest{}, manifestMissing, nil
}

// cursorArtifacts resolves a Cursor plugin payload. Manifest-declared paths
// replace folder discovery for their component; a root SKILL.md is a
// single-skill plugin the flat presentation cannot express and is reported.
func cursorArtifacts(installPath string) (pluginArtifacts, []string) {
	manifest, state, warns := readCursorManifest(installPath)

	if state != manifestParsed {
		return pluginArtifacts{}, warns
	}

	out := pluginArtifacts{
		Skills:   cursorSkills(installPath, manifest, &warns),
		Agents:   cursorFiles(installPath, manifest.Agents, filepath.Join(installPath, agentsDir), cursorAgentExts, &warns),
		Commands: cursorFiles(installPath, manifest.Commands, filepath.Join(installPath, commandsDir), cursorCommandExts, &warns),
	}

	hooks, hookWarns, err := ReadHooksFor(SourceCursor, installPath)
	if err != nil {
		warns = append(warns, warnf(SourceCursor, "plugin %s: %v", installPath, err))
	} else {
		warns = append(warns, hookWarns...)
	}

	mcp, mcpWarns := resolveMCPNames(SourceCursor, installPath)

	warns = append(warns, mcpWarns...)

	out.Hooks = hookEventNames(hooks)
	out.MCP = mcp

	return out, warns
}

func cursorSkills(installPath string, manifest cursorManifest, warns *[]string) []artifactFile {
	if len(manifest.Skills) > 0 {
		return manifestSkillPaths(installPath, SourceCursor, manifest.Skills, warns)
	}

	files := scanSkillDirs(filepath.Join(installPath, skillsDir))

	if len(files) == 0 && isRegularFile(filepath.Join(installPath, skillFile)) {
		*warns = append(*warns, warnf(SourceCursor, "plugin %s: a single-skill root SKILL.md is not presented (add skills/<name>/SKILL.md)", installPath))
	}

	return files
}

func cursorFiles(installPath string, declared []string, dir string, exts []string, warns *[]string) []artifactFile {
	if len(declared) > 0 {
		return manifestFilePaths(installPath, SourceCursor, declared, exts, false, warns)
	}

	return scanNamedFiles(dir, exts, false)
}

var (
	cursorAgentExts   = []string{markdownExt, ".mdc", ".markdown"}
	cursorCommandExts = []string{markdownExt, ".mdc", ".markdown", ".txt"}
)
