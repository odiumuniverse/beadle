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
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

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

// namePattern is the §5.5 plugin name shape.
var namePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)

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

	return renderStdioServer(name, server)
}

func renderRemoteServer(name string, server mcp.Server) (map[string]any, []string, bool) {
	if !strings.HasPrefix(server.URL, "http://") && !strings.HasPrefix(server.URL, "https://") {
		return nil, []string{fmt.Sprintf("mcp %s is skipped: the url %q is not an absolute http(s) url", name, server.URL)}, false
	}

	transport := TransportHTTP
	if server.Transport == mcp.TransportSSE {
		transport = TransportSSE
	}

	entry := map[string]any{"type": transport, "url": server.URL}

	var warnings []string

	if len(server.Headers) > 0 {
		entry["headers"] = server.Headers

		warnings = append(warnings, clientManagedWarnings(name, "headers", server.Headers)...)
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
	}

	if len(server.Env) > 0 {
		entry["env"] = server.Env

		warnings = append(warnings, clientManagedWarnings(name, "env", server.Env)...)
	}

	return entry, warnings, true
}

// portableCommand reports whether a command survives the spec: it is either a
// bare executable name or a ./relative path. Absolute paths and placeholders
// are not portable — command is never interpolated.
func portableCommand(command string) bool {
	switch {
	case command == "", strings.ContainsAny(command, "${}"), strings.HasPrefix(command, "/"):
		return false
	case strings.Contains(command, "/"):
		return strings.HasPrefix(command, "./")
	default:
		return true
	}
}

func envValues(env map[string]string) []string {
	var values []string

	for _, name := range slices.Sorted(maps.Keys(env)) {
		values = append(values, env[name])
	}

	return values
}

// clientManagedWarnings reports the ${VAR} references a client will not
// expand: outside args/env/cwd only ${PLUGIN_ROOT} and ${PLUGIN_DATA} are
// defined, so any other placeholder stays a literal and the client manages the
// authentication itself.
func clientManagedWarnings(name, field string, values map[string]string) []string {
	var warnings []string

	for _, key := range slices.Sorted(maps.Keys(values)) {
		value := values[key]

		for _, ref := range placeholderRefs(value) {
			if ref == pluginRootVar || ref == pluginDataVar {
				continue
			}

			warnings = append(warnings, fmt.Sprintf("mcp %s: %s %s references %s; clients do not expand it — the client manages that authentication", name, field, key, ref))
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

// Write writes the package into dir: the directory is created, beadle's own
// files are rewritten only when their bytes change, beadle-owned files the
// current render no longer produces are removed, and every other file in dir
// is left alone.
func Write(dir string, files Files) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	for _, rel := range slices.Sorted(maps.Keys(files)) {
		target, err := packagePath(dir, rel)
		if err != nil {
			return err
		}

		if existing, err := os.ReadFile(target); err == nil && bytes.Equal(existing, files[rel]) { //nolint:gosec // G304: the path is the package dir the caller passed
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(target), err)
		}

		if err := fsutil.WriteFileAtomic(target, files[rel], 0o600); err != nil {
			return fmt.Errorf("write %s: %w", target, err)
		}
	}

	return pruneOwned(dir, files)
}

// pruneOwned removes the beadle-owned paths the current render does not carry:
// the manifest, the MCP file and everything under skills/. Foreign files stay.
func pruneOwned(dir string, files Files) error {
	for _, rel := range []string{ManifestFile, MCPFile} {
		if _, keep := files[rel]; keep {
			continue
		}

		if err := os.Remove(filepath.Join(dir, rel)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", rel, err)
		}
	}

	skillsDir := filepath.Join(dir, SkillsDir)

	if _, err := os.Stat(skillsDir); errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	var stale []string

	err := filepath.WalkDir(skillsDir, func(p string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}

		rel, relErr := filepath.Rel(dir, p)
		if relErr != nil {
			return relErr
		}

		if _, keep := files[filepath.ToSlash(rel)]; !keep {
			stale = append(stale, p)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("scan %s: %w", SkillsDir, err)
	}

	for _, p := range stale {
		if err := os.Remove(p); err != nil {
			return fmt.Errorf("remove %s: %w", p, err)
		}
	}

	return removeEmptyDirs(skillsDir)
}

func removeEmptyDirs(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil //nolint:nilerr // a directory that cannot be read carries nothing to prune
	}

	empty := true

	for _, entry := range entries {
		if !entry.IsDir() {
			empty = false

			continue
		}

		if err := removeEmptyDirs(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}

		if _, err := os.ReadDir(filepath.Join(dir, entry.Name())); err == nil {
			if remaining, _ := os.ReadDir(filepath.Join(dir, entry.Name())); len(remaining) > 0 {
				empty = false
			}
		}
	}

	if empty {
		_ = os.Remove(dir)
	}

	return nil
}

// packagePath joins a package-relative path to dir and keeps it inside: the
// path must not escape lexically, and its parent must not resolve through a
// symlink out of dir.
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
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return "", fmt.Errorf("create %s: %w", parent, err)
	}

	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", parent, err)
	}

	if resolvedParent != resolvedDir && !strings.HasPrefix(resolvedParent, resolvedDir+string(filepath.Separator)) {
		return "", fmt.Errorf("package path %s resolves outside the output directory", rel)
	}

	return target, nil
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

	if err := json.Unmarshal(raw["name"], &name); err != nil || !namePattern.MatchString(name) {
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

	if err := json.Unmarshal(raw["mcpServers"], &servers); err != nil {
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
		if server["url"] != nil {
			return fmt.Errorf("%s: server %s is stdio and carries a url", MCPFile, name)
		}

		var command string

		if err := json.Unmarshal(server["command"], &command); err != nil || command == "" {
			return fmt.Errorf("%s: server %s needs a non-empty command", MCPFile, name)
		}

		if err := validateStrings(server, name, "args"); err != nil {
			return err
		}
	case TransportHTTP, TransportSSE:
		if server["command"] != nil {
			return fmt.Errorf("%s: server %s is %s and carries a command", MCPFile, name, transport)
		}

		var url string

		if err := json.Unmarshal(server["url"], &url); err != nil ||
			(!strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://")) {
			return fmt.Errorf("%s: server %s needs an absolute http(s) url", MCPFile, name)
		}
	default:
		return fmt.Errorf("%s: server %s has type %q outside the v1 union", MCPFile, name, transport)
	}

	for _, field := range []string{"env", "headers"} {
		values, err := stringMap(server[field])
		if err != nil {
			return fmt.Errorf("%s: server %s: %s must be an object of strings", MCPFile, name, field)
		}

		for key := range values {
			if key == "PLUGIN_ROOT" || key == "PLUGIN_DATA" {
				return fmt.Errorf("%s: server %s: %s is a reserved name", MCPFile, name, key)
			}
		}
	}

	return nil
}

func validateStrings(server map[string]json.RawMessage, name, field string) error {
	raw, ok := server[field]
	if !ok {
		return nil
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
