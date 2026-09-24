package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/skill"
)

// artifactFile is one plugin payload file: the identity beadle presents and
// the absolute path it reads.
type artifactFile struct {
	Name string
	Path string
}

// pluginArtifacts is the resolved payload of one installed plugin: what the
// readers report in the unified model and what the dedup digest hashes. The
// Dir fields are plugin-relative directories the payload lives in: a manifest
// can move them (Cursor, legacy Codex skills), so the farm resolves them
// instead of assuming the well-known names.
type pluginArtifacts struct {
	Skills   []artifactFile
	Agents   []artifactFile
	Commands []artifactFile
	MCP      []string
	Hooks    []string

	SkillDir   string
	AgentDir   string
	CommandDir string
}

func namesOf(files []artifactFile) []string {
	if len(files) == 0 {
		return nil
	}

	names := make([]string, 0, len(files))

	for _, file := range files {
		names = append(names, file.Name)
	}

	slices.Sort(names)

	return names
}

// resolveArtifacts lists the payload of one installed plugin in the layout of
// its host. Unreadable entries become warnings; a missing directory is not a
// warning (most plugins carry only some kinds).
func resolveArtifacts(source, installPath string) (pluginArtifacts, []string) {
	var (
		out   pluginArtifacts
		warns []string
	)

	switch source {
	case SourceCodex:
		out, warns = codexArtifacts(installPath)
	case SourceGeminiCLI:
		out, warns = geminiArtifacts(installPath)
	case SourceAntigravityCLI:
		out, warns = antigravityArtifacts(installPath)
	case SourceCursor:
		out, warns = cursorArtifacts(installPath)
	default:
		out, warns = claudeArtifacts(installPath)
	}

	fillArtifactDirs(installPath, &out)

	return out, warns
}

// fillArtifactDirs records the plugin-relative directories the resolved
// payload lives in, so a caller can rescan the same place a manifest pointed
// the host at.
func fillArtifactDirs(installPath string, out *pluginArtifacts) {
	out.SkillDir = artifactDir(installPath, skillsDir, out.Skills)
	out.AgentDir = artifactDir(installPath, agentsDir, out.Agents)
	out.CommandDir = artifactDir(installPath, commandsDir, out.Commands)
}

// artifactDir returns the install-relative directory a set of payload files
// lives in, or defaultName when the plugin carries none.
func artifactDir(installPath, defaultName string, files []artifactFile) string {
	if len(files) == 0 {
		return defaultName
	}

	rel, err := filepath.Rel(installPath, filepath.Dir(files[0].Path))
	if err != nil || !filepath.IsLocal(rel) {
		return defaultName
	}

	return filepath.ToSlash(rel)
}

// ArtifactDirs returns the plugin-relative payload directories of a source:
// the manifest-declared ones where the host supports them, the well-known
// names otherwise.
func ArtifactDirs(source, installPath string) (skills, agents, commands string) {
	artifacts, _ := resolveArtifacts(source, installPath)

	return artifacts.SkillDir, artifacts.AgentDir, artifacts.CommandDir
}

func claudeArtifacts(installPath string) (pluginArtifacts, []string) {
	out := pluginArtifacts{
		MCP:   scanMCPServers(installPath),
		Hooks: scanHooks(installPath),
	}

	out.Skills = filesIn(filepath.Join(installPath, skillsDir), scanSkills(installPath))
	out.Agents = scanNamedFiles(filepath.Join(installPath, agentsDir), []string{markdownExt}, false)
	out.Commands = filesIn(filepath.Join(installPath, commandsDir), scanCommands(installPath))

	return out, nil
}

// filesIn pairs names with their paths under dir; trim lists the suffixes to
// strip from the stored identity, while the path keeps the original name.
func filesIn(dir string, names []string, trim ...string) []artifactFile {
	files := make([]artifactFile, 0, len(names))

	for _, name := range names {
		identity := name

		for _, suffix := range trim {
			identity = strings.TrimSuffix(identity, suffix)
		}

		files = append(files, artifactFile{Name: identity, Path: filepath.Join(dir, name)})
	}

	return files
}

// scanSkillDirs lists the immediate child directories of dir that are skill
// roots. Symlinks count: the hosts follow them.
func scanSkillDirs(dir string) []artifactFile {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var files []artifactFile

	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())

		info, err := os.Stat(path)
		if err != nil || !info.IsDir() || !skill.HasRoot(path) {
			continue
		}

		files = append(files, artifactFile{Name: entry.Name(), Path: path})
	}

	slices.SortFunc(files, func(a, b artifactFile) int { return strings.Compare(a.Name, b.Name) })

	return files
}

// scanNamedFiles lists the regular files of dir with one of the extensions.
// An empty extension list accepts every regular file and keeps its full name.
func scanNamedFiles(dir string, exts []string, keepExt bool) []artifactFile {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var files []artifactFile

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()

		if len(exts) > 0 && !slices.ContainsFunc(exts, func(ext string) bool { return strings.HasSuffix(name, ext) }) {
			continue
		}

		identity := name
		if !keepExt {
			identity = strings.TrimSuffix(name, filepath.Ext(name))
		}

		files = append(files, artifactFile{Name: identity, Path: filepath.Join(dir, name)})
	}

	slices.SortFunc(files, func(a, b artifactFile) int { return strings.Compare(a.Name, b.Name) })

	return files
}

// localPath resolves one plugin-relative path of a manifest. ok is false when
// the path escapes the plugin root.
func localPath(installPath, rel string) (string, bool) {
	if !filepath.IsLocal(rel) {
		return "", false
	}

	return filepath.Join(installPath, filepath.FromSlash(strings.TrimPrefix(rel, "./"))), true
}

// manifestSkillPaths resolves manifest-declared skill paths: a skill root
// becomes a single skill, a container directory is scanned for child skills.
func manifestSkillPaths(installPath, source string, rels []string, warns *[]string) []artifactFile {
	var files []artifactFile

	for _, rel := range rels {
		dir, ok := localPath(installPath, rel)
		if !ok {
			*warns = append(*warns, warnf(source, "plugin %s: skill path %q escapes the plugin root; ignored", installPath, rel))

			continue
		}

		if skill.HasRoot(dir) {
			files = append(files, artifactFile{Name: filepath.Base(dir), Path: dir})

			continue
		}

		files = append(files, scanSkillDirs(dir)...)
	}

	slices.SortFunc(files, func(a, b artifactFile) int { return strings.Compare(a.Name, b.Name) })

	return files
}

// manifestFilePaths resolves manifest-declared file paths of one kind: a path
// may be a file or a directory holding files of the kind.
func manifestFilePaths(installPath, source string, rels, exts []string, keepExt bool, warns *[]string) []artifactFile {
	var files []artifactFile

	for _, rel := range rels {
		path, ok := localPath(installPath, rel)
		if !ok {
			*warns = append(*warns, warnf(source, "plugin %s: path %q escapes the plugin root; ignored", installPath, rel))

			continue
		}

		info, err := os.Stat(path)
		if err != nil {
			continue
		}

		if info.IsDir() {
			files = append(files, scanNamedFiles(path, exts, keepExt)...)

			continue
		}

		if !info.Mode().IsRegular() {
			continue
		}

		name := filepath.Base(path)
		if !keepExt {
			name = strings.TrimSuffix(name, filepath.Ext(name))
		}

		files = append(files, artifactFile{Name: name, Path: path})
	}

	slices.SortFunc(files, func(a, b artifactFile) int { return strings.Compare(a.Name, b.Name) })

	return files
}

// ArtifactDigest hashes the payload of one installed plugin: the skill trees,
// the agent and command bytes, the MCP server names and the hook event names.
// Two hosts packaging the same plugin with byte-identical portable parts
// compute the same digest, which drives the duplicate-plugin presentation.
// An unreadable payload produces markers instead of a hard failure so a
// broken plugin still cannot crash a sync.
func ArtifactDigest(source, installPath string) (string, error) {
	if installPath == "" || !filepath.IsAbs(installPath) {
		return "", errors.New("plugin install path is empty or not absolute")
	}

	if !isDir(installPath) {
		return "", errors.New("plugin install path is missing")
	}

	artifacts, warns := resolveArtifacts(source, installPath)

	lines := []string{}

	for _, file := range artifacts.Skills {
		lines = append(lines, "skill "+file.Name+" "+treeDigest(file.Path))
	}

	for _, file := range artifacts.Agents {
		lines = append(lines, "agent "+file.Name+" "+fileDigest(file.Path))
	}

	for _, file := range artifacts.Commands {
		lines = append(lines, "command "+logicalName(file.Name)+" "+fileDigest(file.Path))
	}

	for _, name := range artifacts.MCP {
		lines = append(lines, "mcp "+name)
	}

	for _, name := range artifacts.Hooks {
		lines = append(lines, "hook "+name)
	}

	// Names alone would call two plugins identical while their hook commands
	// or MCP server definitions differ: the details join the digest.
	if hooks, _, err := ReadHooksFor(source, installPath); err == nil {
		lines = append(lines, hookDigestLines(hooks)...)
	}

	if data, _, _, ok := MCPDocument(source, installPath); ok {
		lines = append(lines, "mcp-document "+fileDigestBytes(data))
	}

	for _, warn := range warns {
		lines = append(lines, "warn "+strings.ReplaceAll(warn, installPath, ""))
	}

	slices.Sort(lines)

	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))

	return hex.EncodeToString(sum[:]), nil
}

// treeDigest hashes one skill directory.
func treeDigest(dir string) string {
	tree, err := skill.ReadTree(dir)
	if err != nil {
		return "unreadable"
	}

	return string(skill.TreeDigest(tree))
}

// fileDigest hashes one payload file.
func fileDigest(path string) string {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the path is resolved from the caller-provided install path
	if err != nil {
		return "unreadable"
	}

	return fileDigestBytes(data)
}

// fileDigestBytes hashes in-memory payload bytes.
func fileDigestBytes(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

// hookDigestLines renders the hook definitions the digest compares: the event,
// the matcher and every handler field the trust gate reads.
func hookDigestLines(hooks Hooks) []string {
	var lines []string

	for _, event := range slices.Sorted(maps.Keys(hooks)) {
		for _, group := range hooks[event] {
			for _, handler := range group.Hooks {
				lines = append(lines, fmt.Sprintf("hook %s %q %s %q %d %s",
					event, group.Matcher, handler.Type, handler.Command, handler.Timeout,
					strings.Join(handler.Unsupported, ",")))
			}
		}
	}

	return lines
}

// logicalName strips the file extension from a payload name.
func logicalName(name string) string {
	return strings.TrimSuffix(name, filepath.Ext(name))
}

// MCPDocument returns the plugin's MCP server document as JSON bytes with a
// top-level "mcpServers" object. rel is the document path relative to the
// install root, or "" for documents synthesized from a manifest.
func MCPDocument(source, installPath string) (data []byte, rel string, warns []string, ok bool) {
	switch source {
	case SourceCodex:
		return mcpFileDocument(installPath, source, []string{portableMCPFile, mcpFile})
	case SourceCursor:
		return mcpFileDocument(installPath, source, []string{portableMCPFile})
	case SourceAntigravityCLI:
		return mcpFileDocument(installPath, source, []string{agyMCPFile})
	case SourceGeminiCLI:
		return geminiMCPDocument(installPath)
	default:
		return mcpFileDocument(installPath, source, []string{mcpFile})
	}
}

func mcpFileDocument(installPath, source string, candidates []string) ([]byte, string, []string, bool) {
	for _, name := range candidates {
		path := filepath.Join(installPath, name)

		data, err := os.ReadFile(path) //nolint:gosec // G304: the path is built from the caller-provided install path
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}

		if err != nil {
			return nil, name, []string{warnf(source, "plugin %s: cannot read %s: %v", installPath, name, err)}, false
		}

		return data, name, nil, true
	}

	return nil, "", nil, false
}

// mcpServerNames lists the server names of an MCP document.
func mcpServerNames(data []byte) []string {
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}

	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}

	return slices.Sorted(maps.Keys(doc.MCPServers))
}

// hookEventNames lists the event names of one plugin's hook definitions.
func hookEventNames(hooks Hooks) []string {
	return slices.Sorted(maps.Keys(hooks))
}
