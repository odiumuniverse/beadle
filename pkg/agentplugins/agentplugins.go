// Package agentplugins renders the beadle canon as an Agent Plugins v1.0.0
// package: the closed plugin.json schema, the skills trees, and the mcp.json
// wrapper with its closed transport union. The rendering is deterministic and
// idempotent, and every path a server references stays inside the package root.
package agentplugins

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/mcp"
	"github.com/odiumuniverse/beadle/pkg/skill"
)

const (
	// SchemaURL is the Agent Plugins v1.0.0 plugin schema.
	SchemaURL = "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"
	// MCPSchemaURL is the Agent Plugins v1.0.0 mcp schema.
	MCPSchemaURL = "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json"

	// ManifestFile is the package manifest.
	ManifestFile = "plugin.json"
	// MCPFile is the package MCP servers file.
	MCPFile = "mcp.json"
	// SkillsDir is the package skills directory.
	SkillsDir = "skills"
	// SkillFile is the discovery file of an Agent Plugins skill: the directory
	// is found through the immediate child and its SKILL.md, and any other
	// file of the tree is copied along.
	SkillFile = "SKILL.md"

	// TransportStdio, TransportHTTP and TransportSSE are the mcp.json
	// transport types: the closed union of Agent Plugins v1.0.0.
	TransportStdio = "stdio"
	TransportHTTP  = "streamable-http"
	TransportSSE   = "sse"

	pluginRootVar = "${PLUGIN_ROOT}"
	pluginDataVar = "${PLUGIN_DATA}"
)

// namePattern is the §5.5 plugin name shape: 1-64 characters from [a-z0-9.-],
// starting and ending alphanumeric.
var namePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,62}[a-z0-9])?$`)

// validPluginName checks the §5.5 plugin name: the character pattern plus the
// two forbidden sequences. RE2 has no lookahead, so "--" and ".." are checked
// by hand.
func validPluginName(name string) bool {
	if !namePattern.MatchString(name) {
		return false
	}

	return !strings.Contains(name, "--") && !strings.Contains(name, "..")
}

// Manifest is the closed plugin.json schema of Agent Plugins v1.0.0. Client
// data belongs under extensions, keyed by a reverse-domain name.
type Manifest struct {
	Schema      string                     `json:"$schema"`
	Name        string                     `json:"name"`
	Version     string                     `json:"version,omitempty"`
	Description string                     `json:"description,omitempty"`
	Author      json.RawMessage            `json:"author,omitempty"`
	Homepage    string                     `json:"homepage,omitempty"`
	Repository  string                     `json:"repository,omitempty"`
	License     string                     `json:"license,omitempty"`
	Keywords    []string                   `json:"keywords,omitempty"`
	Extensions  map[string]json.RawMessage `json:"extensions,omitempty"`
}

// Options fill the manifest fields the pilot owns.
type Options struct {
	Name        string
	Version     string
	Description string
	License     string
	Keywords    []string
}

// Counts reports how many items of one kind were rendered or skipped.
type Counts struct {
	Rendered int
	Skipped  int
}

// Files maps package-relative paths to their contents.
type Files map[string][]byte

// Package is a rendered Agent Plugins package.
type Package struct {
	Files    Files
	Warnings []string
	Skills   Counts
	MCP      Counts
}

// Render renders the canon into an Agent Plugins package: every skill whose
// tree carries a SKILL.md (the tree itself is copied as-is), and every MCP
// server the closed union can express. The warnings list what was skipped and
// why, plus the non-portable references that were kept unexpanded.
func Render(skills map[string]skill.Tree, servers mcp.Servers, opts Options) (Package, error) {
	pkg := Package{Files: Files{}}

	manifest, err := renderManifest(opts)
	if err != nil {
		return Package{}, err
	}

	pkg.Files[ManifestFile] = manifest

	renderSkills(&pkg, skills)

	if err := renderServers(&pkg, servers); err != nil {
		return Package{}, err
	}

	return pkg, nil
}

func renderManifest(opts Options) ([]byte, error) {
	manifest := Manifest{
		Schema:      SchemaURL,
		Name:        opts.Name,
		Version:     opts.Version,
		Description: opts.Description,
		License:     opts.License,
		Keywords:    opts.Keywords,
	}

	if manifest.Name == "" {
		manifest.Name = "beadle-canon"
	}

	if !validPluginName(manifest.Name) {
		return nil, fmt.Errorf("render %s: %q is not a valid plugin name", ManifestFile, manifest.Name)
	}

	data, err := marshalJSON(manifest)
	if err != nil {
		return nil, fmt.Errorf("render %s: %w", ManifestFile, err)
	}

	return data, nil
}

// renderSkills presents every portable skill. Discovery is non-recursive — a
// skill is an immediate child with a SKILL.md — while the tree behind the
// discovery file is copied along, so a skill keeps its scripts and references.
func renderSkills(pkg *Package, skills map[string]skill.Tree) {
	for _, name := range slices.Sorted(maps.Keys(skills)) {
		if !skill.ValidName(name) {
			skipSkill(pkg, name, "the name is not a valid skill slug")

			continue
		}

		body, ok := skills[name][SkillFile]
		if !ok || len(bytes.TrimSpace(body)) == 0 {
			skipSkill(pkg, name, "the skill has no SKILL.md body")

			continue
		}

		for rel, data := range skills[name] {
			pkg.Files[path.Join(SkillsDir, name, filepath.ToSlash(rel))] = data
		}

		pkg.Skills.Rendered++
	}
}

func skipSkill(pkg *Package, name, reason string) {
	pkg.Skills.Skipped++

	pkg.Warnings = append(pkg.Warnings, fmt.Sprintf("skill %s is skipped: %s", name, reason))
}

// mcpDocument is the mcp.json wrapper of Agent Plugins v1.0.0.
type mcpDocument struct {
	Schema     string         `json:"$schema"`
	MCPServers map[string]any `json:"mcpServers"`
}

func renderServers(pkg *Package, servers mcp.Servers) error {
	rendered := map[string]any{}

	for _, name := range slices.Sorted(maps.Keys(servers)) {
		entry, warnings, ok := renderServer(name, servers[name])

		pkg.Warnings = append(pkg.Warnings, warnings...)

		if !ok {
			pkg.MCP.Skipped++

			continue
		}

		rendered[name] = entry
		pkg.MCP.Rendered++
	}

	if len(rendered) == 0 {
		return nil
	}

	data, err := marshalJSON(mcpDocument{Schema: MCPSchemaURL, MCPServers: rendered})
	if err != nil {
		return fmt.Errorf("render %s: %w", MCPFile, err)
	}

	pkg.Files[MCPFile] = data

	return nil
}

// renderServer renders one canonical server into the closed union. The second
// result carries the skip warning, the third reports whether the server
// rendered at all.
func renderServer(name string, server mcp.Server) (map[string]any, []string, bool) {
	hasURL := server.URL != ""
	hasCommand := len(server.Command) > 0

	switch server.Transport {
	case "", mcp.TransportStdio, mcp.TransportHTTP, mcp.TransportSSE:
	default:
		return nil, []string{fmt.Sprintf("mcp %s is skipped: transport %s is not part of the Agent Plugins v1 union", name, server.Transport)}, false
	}

	if hasURL && hasCommand {
		return nil, []string{fmt.Sprintf("mcp %s is skipped: the server carries both a command and a url", name)}, false
	}

	if hasURL && server.Transport == mcp.TransportStdio {
		return nil, []string{fmt.Sprintf("mcp %s is skipped: the stdio transport carries a url", name)}, false
	}

	if !hasURL && !hasCommand {
		return nil, []string{fmt.Sprintf("mcp %s is skipped: the server has no command", name)}, false
	}

	values := append(slices.Clone(server.Command), envValues(server.Env)...)
	if slices.ContainsFunc(values, escapesRoot) {
		return nil, []string{fmt.Sprintf("mcp %s is skipped: a %s or relative reference escapes the package root", name, pluginRootVar)}, false
	}

	if hasURL {
		return renderRemoteServer(name, server)
	}

	if server.Transport == mcp.TransportHTTP || server.Transport == mcp.TransportSSE {
		return nil, []string{fmt.Sprintf("mcp %s is skipped: the %s transport carries no url", name, server.Transport)}, false
	}

	return renderStdioServer(name, server)
}

func renderRemoteServer(name string, server mcp.Server) (map[string]any, []string, bool) {
	if !validRemoteURL(server.URL) {
		return nil, []string{fmt.Sprintf(
			"mcp %s is skipped: the url %q must be https without user-info or a fragment (http is allowed on loopback only)", name, server.URL)}, false
	}

	transport := TransportHTTP
	if server.Transport == mcp.TransportSSE {
		transport = TransportSSE
	}

	entry := map[string]any{"type": transport, "url": server.URL}

	warnings := clientManagedWarnings(name, "url", []string{server.URL}, false)

	if len(server.Headers) > 0 {
		entry["headers"] = server.Headers
	}

	for _, key := range slices.Sorted(maps.Keys(server.Headers)) {
		warnings = append(warnings, clientManagedWarnings(name, "headers "+key, []string{server.Headers[key]}, false)...)
	}

	return entry, warnings, true
}

func renderStdioServer(name string, server mcp.Server) (map[string]any, []string, bool) {
	command := server.Command[0]

	if !portableCommand(command) {
		return nil, []string{fmt.Sprintf("mcp %s is skipped: the command %q is neither a bare name nor a ./relative path (it is never interpolated)", name, command)}, false
	}

	entry := map[string]any{"type": TransportStdio, "command": command}

	var warnings []string

	if len(server.Command) > 1 {
		entry["args"] = server.Command[1:]

		for i, arg := range server.Command[1:] {
			warnings = append(warnings, clientManagedWarnings(name, fmt.Sprintf("args [%d]", i), []string{arg}, true)...)
		}
	}

	if len(server.Env) > 0 {
		entry["env"] = server.Env
	}

	for _, key := range slices.Sorted(maps.Keys(server.Env)) {
		warnings = append(warnings, clientManagedWarnings(name, "env "+key, []string{server.Env[key]}, true)...)
	}

	return entry, warnings, true
}

// portableCommand reports whether a command survives the spec: it is either a
// bare executable name or a ./relative path, without whitespace or control
// characters. Absolute paths and placeholders are not portable — command is
// never interpolated.
func portableCommand(command string) bool {
	switch {
	case command == "", strings.ContainsAny(command, "${}"), strings.HasPrefix(command, "/"):
		return false
	}

	for _, r := range command {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}

	if strings.Contains(command, "/") {
		return strings.HasPrefix(command, "./")
	}

	return true
}

func envValues(env map[string]string) []string {
	var values []string

	for _, name := range slices.Sorted(maps.Keys(env)) {
		values = append(values, env[name])
	}

	return values
}

// validRemoteURL reports whether a remote url satisfies the schema: an
// absolute https url without user-info or a fragment, or http on a loopback
// host.
func validRemoteURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return false
	}

	switch parsed.Scheme {
	case "https":
		return true
	case "http":
		host := parsed.Hostname()
		if host == "localhost" {
			return true
		}

		ip := net.ParseIP(host)

		return ip != nil && ip.IsLoopback()
	default:
		return false
	}
}

// clientManagedWarnings reports the ${VAR} references a client will not expand.
// The spec defines ${PLUGIN_ROOT}/${PLUGIN_DATA} for args, env and cwd only:
// with expandable false — and for every other name — the reference stays a
// literal and the client manages that authentication itself.
func clientManagedWarnings(name, label string, values []string, expandable bool) []string {
	var warnings []string

	for _, value := range values {
		for _, ref := range placeholderRefs(value) {
			if expandable && (ref == pluginRootVar || ref == pluginDataVar) {
				continue
			}

			warnings = append(warnings, fmt.Sprintf("mcp %s: %s references %s; clients do not expand it — the client manages that authentication", name, label, ref))
		}
	}

	return warnings
}

var placeholderPattern = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*\}`)

func placeholderRefs(value string) []string {
	return placeholderPattern.FindAllString(value, -1)
}

// escapesRoot reports whether a value carries a path that climbs above the
// package root. Every ${PLUGIN_ROOT} reference, every =value tail and every
// path-like fragment is scanned, so a ".." segment cannot hide behind a
// space, a flag or a longer path.
func escapesRoot(value string) bool {
	for fragment := range strings.SplitSeq(value, "=") {
		for part := range strings.SplitSeq(fragment, pluginRootVar) {
			if climbs(part) {
				return true
			}
		}
	}

	return false
}

// climbs reports whether a slash-separated fragment walks above its start.
func climbs(fragment string) bool {
	depth := 0

	for part := range strings.SplitSeq(strings.ReplaceAll(fragment, "\\", "/"), "/") {
		switch part {
		case "", ".":
		case "..":
			depth--

			if depth < 0 {
				return true
			}
		default:
			depth++
		}
	}

	return false
}

// WriteReport lists what a write removed and what it left behind.
type WriteReport struct {
	// Pruned are the package-relative paths the current render no longer
	// carries: the manifest, the MCP file and the discovery file of a skill
	// that is no longer rendered.
	Pruned []string
	// Leftover are the stale skill directories that kept foreign files: the
	// package does not validate until they are removed by hand.
	Leftover []string
}

// Write writes the package into dir: the directory is created, beadle's own
// files are rewritten only when their bytes change, the paths the render no
// longer carries are pruned, and every other file in dir is left alone. The
// report names what was pruned and which stale skill directories kept foreign
// files behind.
func Write(dir string, files Files) (WriteReport, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return WriteReport{}, fmt.Errorf("create %s: %w", dir, err)
	}

	for _, rel := range slices.Sorted(maps.Keys(files)) {
		target, err := packagePath(dir, rel)
		if err != nil {
			return WriteReport{}, err
		}

		if existing, err := os.ReadFile(target); err == nil && bytes.Equal(existing, files[rel]) { //nolint:gosec // G304: the path is the package dir the caller passed
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return WriteReport{}, fmt.Errorf("create %s: %w", filepath.Dir(target), err)
		}

		if err := fsutil.WriteFileAtomic(target, files[rel], 0o600); err != nil {
			return WriteReport{}, fmt.Errorf("write %s: %w", target, err)
		}
	}

	return pruneOwned(dir, files)
}

// pruneOwned removes the paths the current render does not carry: the manifest
// and the MCP file always, and the SKILL.md of any skill name that is no longer
// rendered — beadle cannot tell a foreign skill directory apart from its own
// previous render, so the discovery file is treated as ours. Loose files, stale
// tree files and empty directories stay; a skill directory is removed only when
// removing its discovery file left it empty, and every other stale directory is
// reported as leftover.
func pruneOwned(dir string, files Files) (WriteReport, error) {
	var report WriteReport

	for _, rel := range []string{ManifestFile, MCPFile} {
		if _, keep := files[rel]; keep {
			continue
		}

		switch err := os.Remove(filepath.Join(dir, rel)); {
		case err == nil:
			report.Pruned = append(report.Pruned, rel)
		case errors.Is(err, fs.ErrNotExist):
		default:
			return report, fmt.Errorf("remove %s: %w", rel, err)
		}
	}

	skills, err := pruneStaleSkills(filepath.Join(dir, SkillsDir), files)
	if err != nil {
		return report, err
	}

	report.Pruned = append(report.Pruned, skills.Pruned...)
	report.Leftover = append(report.Leftover, skills.Leftover...)

	return report, nil
}

// pruneStaleSkills removes the discovery file of every skill the current render
// no longer carries; a skill directory is removed only when that left it empty.
func pruneStaleSkills(skillsDir string, files Files) (WriteReport, error) {
	var report WriteReport

	entries, err := os.ReadDir(skillsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return report, nil
	}

	if err != nil {
		return report, fmt.Errorf("read %s: %w", SkillsDir, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()
		if _, keep := files[path.Join(SkillsDir, name, SkillFile)]; keep {
			continue
		}

		skill, err := pruneStaleSkill(skillsDir, name)
		if err != nil {
			return report, err
		}

		report.Pruned = append(report.Pruned, skill.Pruned...)
		report.Leftover = append(report.Leftover, skill.Leftover...)
	}

	return report, nil
}

// pruneStaleSkill removes one stale discovery file. Only a directory whose
// discovery file was actually there counts as a stale skill: without it the
// directory is foreign and stays whole. A directory that keeps other files
// after the removal is reported as leftover — the package does not validate
// until the user cleans it.
func pruneStaleSkill(skillsDir, name string) (WriteReport, error) {
	var report WriteReport

	rel := path.Join(SkillsDir, name)
	discovery := filepath.Join(skillsDir, name, SkillFile)

	switch err := os.Remove(discovery); {
	case err == nil:
		report.Pruned = append(report.Pruned, path.Join(rel, SkillFile))
	case errors.Is(err, fs.ErrNotExist):
		return report, nil
	default:
		return report, fmt.Errorf("remove %s: %w", discovery, err)
	}

	remaining, err := os.ReadDir(filepath.Join(skillsDir, name))
	if err != nil || len(remaining) > 0 {
		report.Leftover = append(report.Leftover, rel)

		return report, nil //nolint:nilerr // a directory that cannot be read carries nothing to remove
	}

	if err := os.Remove(filepath.Join(skillsDir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return report, fmt.Errorf("remove %s: %w", rel, err)
	}

	return report, nil
}

// packagePath joins a package-relative path to dir and keeps it inside: the
// path must not escape lexically, and its deepest existing ancestor must not
// resolve through a symlink out of dir. The check runs before the remaining
// directories are created — MkdirAll would follow the symlink and leave
// directories outside the output root behind.
func packagePath(dir, rel string) (string, error) {
	clean := path.Clean(rel)

	if path.IsAbs(clean) || climbs(clean) {
		return "", fmt.Errorf("package path %s escapes the output directory", rel)
	}

	target := filepath.Join(dir, filepath.FromSlash(clean))

	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", dir, err)
	}

	parent := filepath.Dir(target)

	existing, err := deepestExisting(parent)
	if err != nil {
		return "", err
	}

	resolvedParent, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", existing, err)
	}

	if resolvedParent != resolvedDir && !strings.HasPrefix(resolvedParent, resolvedDir+string(filepath.Separator)) {
		return "", fmt.Errorf("package path %s resolves outside the output directory", rel)
	}

	if err := os.MkdirAll(parent, 0o750); err != nil {
		return "", fmt.Errorf("create %s: %w", parent, err)
	}

	return target, nil
}

// deepestExisting returns the longest existing ancestor of path, path itself
// included when it exists.
func deepestExisting(path string) (string, error) {
	for current := path; ; {
		if _, err := os.Lstat(current); err == nil {
			return current, nil
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no existing ancestor for %s", path)
		}

		current = parent
	}
}

// Validate checks a rendered package against the closed v1.0.0 schema: the
// manifest keys, the skills discovery shape, and the mcp.json wrapper with its
// closed transport union.
func Validate(dir string) error {
	if err := validateManifest(filepath.Join(dir, ManifestFile)); err != nil {
		return err
	}

	if err := validateSkills(filepath.Join(dir, SkillsDir)); err != nil {
		return err
	}

	return validateMCP(filepath.Join(dir, MCPFile))
}

//nolint:cyclop,gocyclo // the closed schema checks read better inline
func validateManifest(path string) error {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the path is the package dir the caller passed
	if err != nil {
		return fmt.Errorf("read %s: %w", ManifestFile, err)
	}

	var raw map[string]json.RawMessage

	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("%s: %w", ManifestFile, err)
	}

	allowed := map[string]struct{}{
		"$schema": {}, "name": {}, "version": {}, "description": {}, "author": {},
		"homepage": {}, "repository": {}, "license": {}, "keywords": {}, "extensions": {},
	}

	for key := range raw {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("%s: unknown key %q", ManifestFile, key)
		}
	}

	// §5.3 requires $schema and name; version is optional.
	var schema, name string

	if err := json.Unmarshal(raw["$schema"], &schema); err != nil || schema != SchemaURL {
		return fmt.Errorf("%s: %q must be %q", ManifestFile, "$schema", SchemaURL)
	}

	if err := json.Unmarshal(raw["name"], &name); err != nil || !validPluginName(name) {
		return fmt.Errorf("%s: %q must match the plugin name pattern", ManifestFile, "name")
	}

	if version, ok := raw["version"]; ok {
		var value string

		if err := json.Unmarshal(version, &value); err != nil || value == "" {
			return fmt.Errorf("%s: %q must be a non-empty string", ManifestFile, "version")
		}
	}

	var keywords []string

	if err := json.Unmarshal(raw["keywords"], &keywords); err != nil && raw["keywords"] != nil {
		return fmt.Errorf("%s: %q must be an array of strings", ManifestFile, "keywords")
	}

	return nil
}

func validateSkills(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("read %s: %w", SkillsDir, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			return fmt.Errorf("%s: %s is not a skill directory", SkillsDir, entry.Name())
		}

		root := filepath.Join(dir, entry.Name())

		names, err := os.ReadDir(root)
		if err != nil {
			return fmt.Errorf("read %s: %w", SkillsDir, err)
		}

		found := false

		for _, name := range names {
			if name.Name() == SkillFile && name.Type().IsRegular() {
				found = true
			}
		}

		if !found {
			return fmt.Errorf("%s/%s: the skill has no regular %s", SkillsDir, entry.Name(), SkillFile)
		}
	}

	return nil
}

func validateMCP(path string) error {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the path is the package dir the caller passed
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("read %s: %w", MCPFile, err)
	}

	var raw map[string]json.RawMessage

	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("%s: %w", MCPFile, err)
	}

	for key := range raw {
		if key != "$schema" && key != "mcpServers" {
			return fmt.Errorf("%s: unknown top-level key %q", MCPFile, key)
		}
	}

	var schema string

	if err := json.Unmarshal(raw["$schema"], &schema); err != nil || schema != MCPSchemaURL {
		return fmt.Errorf("%s: %q must be %q", MCPFile, "$schema", MCPSchemaURL)
	}

	var servers map[string]map[string]json.RawMessage

	if isJSONNull(raw["mcpServers"]) || json.Unmarshal(raw["mcpServers"], &servers) != nil {
		return fmt.Errorf("%s: %q must be an object of servers", MCPFile, "mcpServers")
	}

	for _, name := range slices.Sorted(maps.Keys(servers)) {
		if err := validateMCPServer(name, servers[name]); err != nil {
			return err
		}
	}

	return nil
}

// validateMCPServer checks one server against the closed union.
//
//nolint:cyclop,gocyclo // the closed union checks read better inline
func validateMCPServer(name string, server map[string]json.RawMessage) error {
	allowed := map[string]struct{}{
		"type": {}, "command": {}, "args": {}, "env": {}, "cwd": {}, "url": {}, "headers": {},
	}

	for key := range server {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("%s: server %s has unknown key %q", MCPFile, name, key)
		}
	}

	var transport string

	if err := json.Unmarshal(server["type"], &transport); err != nil {
		return fmt.Errorf("%s: server %s needs a type", MCPFile, name)
	}

	switch transport {
	case TransportStdio:
		if err := rejectFields(server, name, "is stdio and carries a", "url", "headers"); err != nil {
			return err
		}

		var command string

		if err := json.Unmarshal(server["command"], &command); err != nil || command == "" {
			return fmt.Errorf("%s: server %s needs a non-empty command", MCPFile, name)
		}

		if err := validateStrings(server, name, "args"); err != nil {
			return err
		}

		if err := validateCWD(server, name); err != nil {
			return err
		}

		if err := validateReservedEnv(server, name); err != nil {
			return err
		}

		if _, err := stringMap(server["env"]); err != nil {
			return fmt.Errorf("%s: server %s: env must be an object of strings", MCPFile, name)
		}
	case TransportHTTP, TransportSSE:
		if err := rejectFields(server, name, "is "+transport+" and carries a", "command", "args", "env", "cwd"); err != nil {
			return err
		}

		var url string

		if err := json.Unmarshal(server["url"], &url); err != nil || !validRemoteURL(url) {
			return fmt.Errorf("%s: server %s needs an https url without user-info or a fragment (http is allowed on loopback only)", MCPFile, name)
		}

		if _, err := stringMap(server["headers"]); err != nil {
			return fmt.Errorf("%s: server %s: headers must be an object of strings", MCPFile, name)
		}
	default:
		return fmt.Errorf("%s: server %s has type %q outside the v1 union", MCPFile, name, transport)
	}

	return nil
}

// rejectFields rejects the fields the schema does not define for a variant.
func rejectFields(server map[string]json.RawMessage, name, verb string, fields ...string) error {
	for _, field := range fields {
		if server[field] != nil {
			return fmt.Errorf("%s: server %s %s %s", MCPFile, name, verb, field)
		}
	}

	return nil
}

// validateCWD checks the cwd forms the schema defines: a ./-relative path, a
// ${PLUGIN_ROOT} path or a ${PLUGIN_DATA} path, each staying inside its root.
// The pilot renders no cwd, so this guards foreign packages.
func validateCWD(server map[string]json.RawMessage, name string) error {
	raw, ok := server["cwd"]
	if !ok {
		return nil
	}

	var value string

	if err := json.Unmarshal(raw, &value); err != nil || !validCWD(value) {
		return fmt.Errorf("%s: server %s: cwd must be a ./-relative, %s or %s path inside its root",
			MCPFile, name, pluginRootVar, pluginDataVar)
	}

	return nil
}

// validCWD reports whether a cwd stays inside its root in one of the three
// schema forms.
func validCWD(value string) bool {
	for _, prefix := range []string{pluginRootVar, pluginDataVar} {
		if rest, ok := strings.CutPrefix(value, prefix); ok {
			return rest == "" || (strings.HasPrefix(rest, "/") && !climbs(rest))
		}
	}

	rest, ok := strings.CutPrefix(value, "./")

	return ok && rest != "" && !climbs(rest)
}

// validateReservedEnv rejects the two names the spec reserves in env.
func validateReservedEnv(server map[string]json.RawMessage, name string) error {
	values, err := stringMap(server["env"])
	if err != nil {
		return fmt.Errorf("%s: server %s: env must be an object of strings", MCPFile, name)
	}

	for key := range values {
		if key == "PLUGIN_ROOT" || key == "PLUGIN_DATA" {
			return fmt.Errorf("%s: server %s: %s is a reserved name", MCPFile, name, key)
		}
	}

	return nil
}

// isJSONNull reports whether a raw value is the JSON null literal: the shallow
// type checks would otherwise accept null as an empty object or array.
func isJSONNull(raw json.RawMessage) bool {
	return len(raw) > 0 && string(bytes.TrimSpace(raw)) == "null"
}

func validateStrings(server map[string]json.RawMessage, name, field string) error {
	raw, ok := server[field]
	if !ok {
		return nil
	}

	if isJSONNull(raw) {
		return fmt.Errorf("%s: server %s: %s must be an array of strings", MCPFile, name, field)
	}

	var values []string

	if err := json.Unmarshal(raw, &values); err != nil {
		return fmt.Errorf("%s: server %s: %s must be an array of strings", MCPFile, name, field)
	}

	return nil
}

func stringMap(raw json.RawMessage) (map[string]string, error) {
	if raw == nil {
		return nil, nil
	}

	if isJSONNull(raw) {
		return nil, errors.New("the value is null")
	}

	var values map[string]string

	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}

	return values, nil
}

func marshalJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}

	return append(data, '\n'), nil
}
