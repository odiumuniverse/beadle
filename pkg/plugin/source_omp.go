package plugin

import (
	"cmp"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/agent"
)

const (
	// ompPluginMetaDir is omp's own plugin manifest directory. omp reads it,
	// but it reads the version from .claude-plugin/plugin.json only: a plugin
	// that ships the .omp-plugin manifest alone installs as 0.0.0, so this
	// reader never takes a version from it either.
	ompPluginMetaDir = ".omp-plugin"
	// ompPackageFile is the manifest of an npm/linked omp plugin: `omp plugin
	// link` refuses a directory without it.
	ompPackageFile = "package.json"
	// ompNodeModulesDir holds the symlinks of every installed omp plugin:
	// marketplace installs point into plugins/cache, linked/npm packages point
	// at their source directory.
	ompNodeModulesDir = "node_modules"
)

// OmpRoot returns omp's state root, the directory that holds the plugin state
// (marketplaces.json and plugins/): ~/.omp by default, <home>/<PI_CONFIG_DIR>
// when PI_CONFIG_DIR names another root, and the profile directory when a
// named profile is active (live-verified on 18.4.1: with OMP_PROFILE=work omp
// reads ~/.omp/profiles/work/plugins/installed_plugins.json).
//
// The resolution lives in pkg/agent (agent.OmpStateRoot), which already
// resolves the same root for the agent surfaces: a second copy of the rule
// here is what let the two drift as soon as a profile was active.
func OmpRoot(home string) string {
	return agent.OmpStateRoot(home)
}

// ompInstalled is omp's installed_plugins.json (version 2): the marketplace
// installs keyed by name@marketplace, each with the resolved install path and
// the version omp recorded.
type ompInstalled struct {
	Plugins map[string][]installRecord `json:"plugins"`
}

// readOMP reads the plugins omp installed into its user root. omp keeps no
// "available but not installed" list: the installs are named by
// plugins/installed_plugins.json, and the remaining node_modules symlinks are
// the linked and npm packages. Beadle never writes this tree: omp's installer
// holds no cross-process lock, so a hand-written file could overwrite a
// concurrent install.
func readOMP(home string) ([]Plugin, []string) {
	root := agent.OmpPluginsDir(home)
	if !isDir(root) {
		return nil, nil
	}

	registry, warns := readOMPInstalled(root)

	var plugins []Plugin

	covered := map[string]struct{}{}

	for _, key := range slices.Sorted(maps.Keys(registry)) {
		name, marketplace, ok := strings.Cut(key, "@")
		if !ok || name == "" || marketplace == "" {
			warns = append(warns, warnf(SourceOMP, "malformed plugin key %s", key))

			continue
		}

		for _, record := range registry[key] {
			plugin, ok, pluginWarns := ompRegistryPlugin(marketplace, name, record)

			warns = append(warns, pluginWarns...)

			if !ok {
				continue
			}

			covered[plugin.Name] = struct{}{}

			plugins = append(plugins, plugin)
		}
	}

	linked, linkedWarns := readOMPNodeModules(root, covered)

	plugins = append(plugins, linked...)
	warns = append(warns, linkedWarns...)

	return plugins, warns
}

// readOMPInstalled reads the user-scope marketplace registry. A missing file
// is not a warning: an omp install without marketplace plugins has none.
func readOMPInstalled(root string) (map[string][]installRecord, []string) {
	var doc ompInstalled

	path := filepath.Join(root, installedFile)

	state, warns := readJSONManifest(path, SourceOMP, &doc)
	if state == manifestMissing {
		return nil, nil
	}

	if state != manifestParsed {
		return nil, warns
	}

	return doc.Plugins, warns
}

// ompRegistryPlugin turns one registry record into a plugin. omp records the
// resolved install path and the version at install time, so the registry is
// the source of truth and the manifest only fills name and description: the
// version omp actually installs is the one in .claude-plugin/plugin.json.
func ompRegistryPlugin(marketplace, name string, record installRecord) (Plugin, bool, []string) {
	plugin := Plugin{
		Source:      SourceOMP,
		Origin:      marketplace,
		Name:        name,
		Version:     record.Version,
		Scope:       fallback(record.Scope, scopeUser),
		InstallPath: record.InstallPath,
	}

	if record.InstallPath == "" || !isDir(record.InstallPath) {
		return plugin, false, []string{warnf(SourceOMP, "installed plugin %s@%s %s: %s", name, marketplace, missingDirNote, record.InstallPath)}
	}

	meta, state, warns := readOMPManifest(record.InstallPath)

	if state == manifestParsed {
		plugin.Name = fallback(meta.Name, plugin.Name)
		plugin.Description = meta.Description
	}

	plugin.fillArtifacts(SourceOMP, &warns)

	return plugin, true, warns
}

// readOMPNodeModules reads the omp plugins that only exist as a node_modules
// symlink: the packages `omp plugin link` or an npm install put there. A
// marketplace install is already covered by the registry under the same
// package name, so it is skipped here.
func readOMPNodeModules(root string, covered map[string]struct{}) ([]Plugin, []string) {
	dir := filepath.Join(root, ompNodeModulesDir)

	var (
		plugins []Plugin
		warns   []string
	)

	for _, name := range subdirs(dir) {
		if _, ok := covered[name]; ok {
			continue
		}

		installPath := filepath.Join(dir, name)

		meta, state, manifestWarns := readOMPPackageManifest(installPath)
		if state == manifestMissing {
			// A node_modules entry without package.json cannot be linked by
			// omp; fall back to the plugin manifests for a registry-less
			// marketplace copy.
			meta, state, manifestWarns = readOMPManifest(installPath)
		}

		warns = append(warns, manifestWarns...)

		if state == manifestMissing {
			warns = append(warns, warnf(SourceOMP, "linked plugin %s has no %s; skipped", name, ompPackageFile))

			continue
		}

		if state != manifestParsed {
			continue
		}

		plugin := Plugin{
			Source: SourceOMP,
			// A linked package has no marketplace, so the host id names its
			// namespace, as it does for the other hosts without marketplaces.
			Origin:      SourceOMP,
			Name:        fallback(meta.Name, name),
			Version:     meta.Version,
			Description: meta.Description,
			Scope:       scopeUser,
			InstallPath: installPath,
		}

		plugin.fillArtifacts(SourceOMP, &warns)

		plugins = append(plugins, plugin)
	}

	return plugins, warns
}

// ompHookPhases are the two module directories omp discovers under hooks/: a
// file directly in hooks/ is ignored, and so is anything nested deeper.
var ompHookPhases = []string{"pre", "post"}

// ompHookModuleExts are the module extensions omp auto-discovers.
var ompHookModuleExts = []string{".ts", ".js"}

// ompArtifacts resolves an omp plugin payload: skills/<name>/SKILL.md,
// agents/*.md, commands/*.md (omp loads a non-recursive *.md glob), the
// Claude-compatible .mcp.json, and the hook modules. omp has no command-hook
// file, so the declarative Hooks list stays empty and the modules are the
// separate surface below. rules/, tools/ and package.json#omp.extensions carry
// no unified kind and are not presented either.
func ompArtifacts(installPath string) (pluginArtifacts, []string) {
	modules, moduleWarns := HookModules(SourceOMP, installPath)

	out := pluginArtifacts{
		Skills:      scanSkillDirs(filepath.Join(installPath, skillsDir)),
		Agents:      scanNamedFiles(filepath.Join(installPath, agentsDir), []string{markdownExt}, false),
		Commands:    scanNamedFiles(filepath.Join(installPath, commandsDir), []string{markdownExt}, false),
		HookModules: modules,
	}

	warns := slices.Clone(moduleWarns)

	hooks, hookWarns, err := ReadHooksFor(SourceOMP, installPath)
	if err != nil {
		warns = append(warns, warnf(SourceOMP, "plugin %s: %v", installPath, err))
	} else {
		warns = append(warns, hookWarns...)
	}

	mcp, mcpWarns := resolveMCPNames(SourceOMP, installPath)

	warns = append(warns, mcpWarns...)

	out.Hooks = hookEventNames(hooks)
	out.MCP = mcp

	return out, warns
}

// HookModules lists the hook module files one installed plugin carries for its
// host: omp's hooks/{pre,post}/*.{ts,js}. Every other host has no module
// surface, so the result is empty. The files are the deliverable artifacts of
// the module surface: beadle copies them byte-for-byte, so the caller reads
// each Path and hashes the bytes.
func HookModules(source, installPath string) ([]HookModuleFile, []string) {
	if source != SourceOMP {
		return nil, nil
	}

	var (
		modules []HookModuleFile
		warns   []string
	)

	for _, phase := range ompHookPhases {
		dir := filepath.Join(installPath, hooksDir, phase)

		entries, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}

			warns = append(warns, warnf(SourceOMP, "plugin %s: cannot read hooks/%s: %v", installPath, phase, err))

			continue
		}

		for _, entry := range entries {
			name := entry.Name()

			switch {
			case entry.IsDir():
				warns = append(warns, warnf(SourceOMP,
					"plugin %s: hooks/%s/%s is a directory; omp loads files only", installPath, phase, name))
			case !slices.Contains(ompHookModuleExts, filepath.Ext(name)):
				warns = append(warns, warnf(SourceOMP,
					"plugin %s: hooks/%s/%s is not a .ts/.js module omp discovers; not delivered", installPath, phase, name))
			default:
				modules = append(modules, HookModuleFile{Phase: phase, Name: name, Path: filepath.Join(dir, name)})
			}
		}
	}

	// pre loads before post, so it sorts first; within a phase the names sort.
	slices.SortFunc(modules, func(a, b HookModuleFile) int {
		return cmp.Or(cmp.Compare(slices.Index(ompHookPhases, a.Phase), slices.Index(ompHookPhases, b.Phase)),
			strings.Compare(a.Name, b.Name))
	})

	return modules, warns
}

// ompPluginMetaCandidate is the position of .omp-plugin/plugin.json in the
// omp manifest candidates: the only one omp reads without taking a version.
const ompPluginMetaCandidate = 2

// readOMPPackageManifest reads the package.json of an npm/linked omp plugin.
// `omp plugin list` reports that manifest's version, so it is the version omp
// itself serves for a linked package.
func readOMPPackageManifest(dir string) (pluginMeta, manifestState, []string) {
	var meta pluginMeta

	state, warns := readJSONManifest(filepath.Join(dir, ompPackageFile), SourceOMP, &meta)

	return meta, state, warns
}

// readOMPManifest reads the manifest of one omp plugin directory. The
// .claude-plugin/plugin.json omp installs from wins, then the package.json of
// an npm/linked package, then the .omp-plugin/plugin.json omp reads for
// identity only. The version always comes from one of the first two: omp
// reports 0.0.0 for a plugin that declares its version only in .omp-plugin.
func readOMPManifest(dir string) (pluginMeta, manifestState, []string) {
	candidates := []string{
		filepath.Join(dir, metaDir, metaFile),
		filepath.Join(dir, ompPackageFile),
		filepath.Join(dir, ompPluginMetaDir, metaFile),
	}

	for i, path := range candidates {
		var meta pluginMeta

		state, warns := readJSONManifest(path, SourceOMP, &meta)
		if state == manifestMissing {
			continue
		}

		if state != manifestParsed {
			return pluginMeta{}, state, warns
		}

		if i == ompPluginMetaCandidate {
			// omp never reads a version from .omp-plugin/plugin.json: a plugin
			// that declares it only there installs as 0.0.0.
			meta.Version = ""
		}

		return meta, manifestParsed, warns
	}

	return pluginMeta{}, manifestMissing, nil
}
