package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/hooks"
	"github.com/odiumuniverse/beadle/pkg/kind"
)

type Host string

const (
	Claude      Host = "claude"
	Gemini      Host = "gemini"
	Antigravity Host = "antigravity"
	Omp         Host = "omp"

	MarketplaceName  = "beadle"
	PluginName       = "beadle-canon"
	versionPrefix    = "0.0.0-"
	keyName          = "name"
	keyDescription   = "description"
	keyVersion       = "version"
	keyMCPServers    = "mcpServers"
	canonDescription = "beadle vault canon: skills, MCP servers and approved hooks"
)

func Hosts() []Host {
	return []Host{Claude, Gemini, Antigravity, Omp}
}

func ParseHost(name string) (Host, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "claude", "claude-code":
		return Claude, nil
	case "gemini", "gemini-cli":
		return Gemini, nil
	case "antigravity", "antigravity-cli", "agy":
		return Antigravity, nil
	case "omp", "oh-my-pi":
		return Omp, nil
	default:
		return "", fmt.Errorf("unknown bundle host %q (expected claude, gemini, antigravity or omp)", name)
	}
}

func (h Host) AgentID() string {
	switch h {
	case Claude:
		return agent.ClaudeCodeID
	case Gemini:
		return agent.GeminiCLIID
	case Omp:
		return agent.OmpID
	default:
		return agent.AntigravityCLIID
	}
}

func (h Host) Kinds() []kind.ID {
	if h == Claude || h == Omp {
		return []kind.ID{kind.Skills, kind.MCP}
	}

	return []kind.ID{kind.MCP}
}

func (h Host) ContentKinds() []kind.ID {
	if h == Gemini {
		return []kind.ID{kind.MCP}
	}

	return []kind.ID{kind.Skills, kind.MCP}
}

func (h Host) Binary() string {
	switch h {
	case Claude:
		return "claude"
	case Gemini:
		return "gemini"
	case Omp:
		return "omp"
	default:
		return "agy"
	}
}

type Request struct {
	Host     Host
	Skills   map[string]map[string][]byte
	Servers  kind.Items
	Hooks    map[string]hooks.Hook
	Approved map[string]bool
}

type Result struct {
	Version  string
	Changed  bool
	Warnings []string
}

// renderSeam is a test-only hook over the rendered file set. Production
// leaves it nil.
var renderSeam func(Host, map[string][]byte)

func Plan(req Request) (Result, map[string][]byte, error) {
	files, warns, err := render(req)
	if err != nil {
		return Result{}, nil, err
	}

	// renderSeam lets tests perturb the rendered bytes; the hash below must
	// follow the bytes, so any format change still reaches the host cache.
	if renderSeam != nil {
		renderSeam(req.Host, files)
	}

	version := contentVersion(req.Host, files)

	if err := stampBundleVersion(req.Host, version, files); err != nil {
		return Result{}, nil, err
	}

	return Result{Version: version, Warnings: warns}, files, nil
}

// stampBundleVersion writes the rendered version into every document of the
// bundle the host reads it from: the plugin manifest's version field, and the
// catalog plugin entry the host compares on upgrade. A host with no version
// document (Antigravity) stamps nothing.
func stampBundleVersion(host Host, version string, files map[string][]byte) error {
	documents := map[Host][]string{
		Claude: {".claude-plugin/marketplace.json", "plugins/" + PluginName + "/.claude-plugin/plugin.json"},
		Gemini: {"gemini-extension.json"},
		Omp:    {".omp-plugin/marketplace.json", "plugins/" + PluginName + "/.claude-plugin/plugin.json"},
	}

	for _, rel := range documents[host] {
		if err := stampVersion(rel, version, files); err != nil {
			return err
		}
	}

	return nil
}

// stampVersion writes version into one bundle document: the first catalog
// plugin entry when the document is a catalog, the top-level version field
// otherwise.
func stampVersion(rel, version string, files map[string][]byte) error {
	doc := map[string]any{}

	if err := json.Unmarshal(files[rel], &doc); err != nil {
		return fmt.Errorf("stamp %s: %w", rel, err)
	}

	if plugins, ok := doc["plugins"].([]any); ok && len(plugins) > 0 {
		if entry, _ := plugins[0].(map[string]any); entry != nil {
			entry[keyVersion] = version
		}
	} else {
		doc[keyVersion] = version
	}

	data, err := encodeJSON(doc)
	if err != nil {
		return err
	}

	files[rel] = data

	return nil
}

func Render(root string, req Request) (Result, error) {
	result, files, err := Plan(req)
	if err != nil {
		return Result{}, err
	}

	changed, err := writeTree(filepath.Join(root, string(req.Host)), files)
	if err != nil {
		return Result{}, err
	}

	result.Changed = changed

	return result, nil
}

func contentVersion(host Host, files map[string][]byte) string {
	hash := sha256.New()

	fmt.Fprintf(hash, "host\x00%s\n", host)

	for _, rel := range slices.Sorted(maps.Keys(files)) {
		fmt.Fprintf(hash, "file\x00%s\x00", rel)
		hash.Write(files[rel])
		hash.Write([]byte{'\n'})
	}

	return versionPrefix + hex.EncodeToString(hash.Sum(nil))[:12]
}

func render(req Request) (map[string][]byte, []string, error) {
	files := map[string][]byte{}

	servers, err := agent.EncodeMCPServers(req.Host.AgentID(), req.Servers)
	if err != nil {
		return nil, nil, err
	}

	hookDoc, warns, err := renderHooks(req)
	if err != nil {
		return nil, nil, err
	}

	switch req.Host {
	case Claude:
		if err := addClaudePlugin(files, req, servers, hookDoc); err != nil {
			return nil, nil, err
		}
	case Gemini:
		if err := addGeminiExtension(files, servers, hookDoc); err != nil {
			return nil, nil, err
		}
	case Antigravity:
		if err := addAntigravityPlugin(files, req, servers, hookDoc); err != nil {
			return nil, nil, err
		}
	case Omp:
		if err := addOmpPlugin(files, req, servers); err != nil {
			return nil, nil, err
		}
	default:
		return nil, nil, fmt.Errorf("unsupported bundle host %q", req.Host)
	}

	return files, warns, nil
}

// addClaudePlugin renders the Claude marketplace, the plugin manifest, the
// plugin MCP document and the plugin hooks document.
func addClaudePlugin(files map[string][]byte, req Request, servers map[string]any, hookDoc []byte) error {
	marketplace, err := encodeJSON(map[string]any{
		keyName:        MarketplaceName,
		"owner":        map[string]any{keyName: MarketplaceName},
		keyDescription: canonDescription,
		"plugins": []any{map[string]any{
			keyName:  PluginName,
			"source": "./plugins/" + PluginName,
		}},
	})
	if err != nil {
		return err
	}

	manifest, err := encodeJSON(map[string]any{
		keyName:        PluginName,
		keyDescription: canonDescription,
		"author":       map[string]any{keyName: MarketplaceName},
	})
	if err != nil {
		return err
	}

	mcpDoc, err := encodeJSON(map[string]any{keyMCPServers: servers})
	if err != nil {
		return err
	}

	files[".claude-plugin/marketplace.json"] = marketplace
	files["plugins/"+PluginName+"/.claude-plugin/plugin.json"] = manifest
	files["plugins/"+PluginName+"/.mcp.json"] = mcpDoc
	files["plugins/"+PluginName+"/hooks/hooks.json"] = hookDoc

	addSkills(files, "plugins/"+PluginName+"/skills", req.Skills)

	return nil
}

// addAntigravityPlugin renders the Antigravity plugin manifest (name and
// description only: the schema forbids extra properties), the MCP document
// and the owner-keyed hooks document.
func addAntigravityPlugin(files map[string][]byte, req Request, servers map[string]any, hookDoc []byte) error {
	manifest, err := encodeJSON(map[string]any{keyName: PluginName, keyDescription: canonDescription})
	if err != nil {
		return err
	}

	mcpDoc, err := encodeJSON(map[string]any{keyMCPServers: servers})
	if err != nil {
		return err
	}

	files["plugin.json"] = manifest
	files["mcp_config.json"] = mcpDoc
	files["hooks.json"] = hookDoc

	addSkills(files, "skills", req.Skills)

	return nil
}

// addGeminiExtension renders the Gemini extension manifest (the MCP servers
// inline) plus its hooks document.
func addGeminiExtension(files map[string][]byte, servers map[string]any, hookDoc []byte) error {
	manifest, err := encodeJSON(map[string]any{
		keyName:        PluginName,
		keyDescription: "beadle vault canon: MCP servers and approved hooks",
		keyMCPServers:  servers,
	})
	if err != nil {
		return err
	}

	files["gemini-extension.json"] = manifest
	files["hooks/hooks.json"] = hookDoc

	return nil
}

// addOmpPlugin renders the native omp bundle: the .omp-plugin catalog with the
// plugin tree beside it. The plugin manifest stays in .claude-plugin/ because
// that is the only manifest omp reads a version from, and no hooks file is
// written: omp hooks are code modules, not a command document.
func addOmpPlugin(files map[string][]byte, req Request, servers map[string]any) error {
	marketplace, err := encodeJSON(map[string]any{
		keyName:        MarketplaceName,
		"owner":        map[string]any{keyName: MarketplaceName},
		keyDescription: canonDescription,
		"plugins": []any{map[string]any{
			keyName:  PluginName,
			"source": "./plugins/" + PluginName,
		}},
	})
	if err != nil {
		return err
	}

	manifest, err := encodeJSON(map[string]any{
		keyName:        PluginName,
		keyDescription: canonDescription,
		"author":       map[string]any{keyName: MarketplaceName},
	})
	if err != nil {
		return err
	}

	mcpDoc, err := encodeJSON(map[string]any{keyMCPServers: servers})
	if err != nil {
		return err
	}

	files[".omp-plugin/marketplace.json"] = marketplace
	files["plugins/"+PluginName+"/.claude-plugin/plugin.json"] = manifest
	files["plugins/"+PluginName+"/.mcp.json"] = mcpDoc

	addSkills(files, "plugins/"+PluginName+"/skills", req.Skills)

	return nil
}

func addSkills(files map[string][]byte, prefix string, skills map[string]map[string][]byte) {
	for _, name := range slices.Sorted(maps.Keys(skills)) {
		tree := skills[name]

		for _, rel := range slices.Sorted(maps.Keys(tree)) {
			files[prefix+"/"+name+"/"+rel] = tree[rel]
		}
	}
}

func encodeJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode bundle manifest: %w", err)
	}

	return append(data, '\n'), nil
}

func writeTree(root string, files map[string][]byte) (bool, error) {
	existing, err := scanTree(root)
	if err != nil {
		return false, err
	}

	changed, err := treeChanged(root, files, existing)
	if err != nil {
		return false, err
	}

	if !changed {
		return false, nil
	}

	if err := writeFiles(root, files); err != nil {
		return false, err
	}

	if err := removeStale(root, files, existing); err != nil {
		return false, err
	}

	pruneEmptyDirs(root)

	return true, nil
}

func scanTree(root string) (map[string]struct{}, error) {
	existing := map[string]struct{}{}

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		existing[filepath.ToSlash(rel)] = struct{}{}

		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("scan bundle: %w", err)
	}

	return existing, nil
}

func treeChanged(root string, files map[string][]byte, existing map[string]struct{}) (bool, error) {
	if len(existing) != len(files) {
		return true, nil
	}

	for rel, data := range files {
		if _, ok := existing[rel]; !ok {
			return true, nil
		}

		current, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))) //nolint:gosec // G304: rel comes from our own file map
		if err != nil {
			return false, fmt.Errorf("read bundle file: %w", err)
		}

		if !slices.Equal(current, data) {
			return true, nil
		}
	}

	return false, nil
}

func writeFiles(root string, files map[string][]byte) error {
	for rel, data := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))

		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return fmt.Errorf("create bundle directory: %w", err)
		}

		if err := fsutil.WriteFileAtomic(path, data, 0o600); err != nil {
			return fmt.Errorf("write bundle file: %w", err)
		}
	}

	return nil
}

func removeStale(root string, files map[string][]byte, existing map[string]struct{}) error {
	for rel := range existing {
		if _, ok := files[rel]; ok {
			continue
		}

		if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale bundle file: %w", err)
		}
	}

	return nil
}

func pruneEmptyDirs(root string) {
	var dirs []string

	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && entry.IsDir() && path != root {
			dirs = append(dirs, path)
		}

		return nil
	})

	slices.SortFunc(dirs, func(a, b string) int {
		return strings.Count(b, string(filepath.Separator)) - strings.Count(a, string(filepath.Separator))
	})

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err == nil && len(entries) == 0 {
			_ = os.Remove(dir)
		}
	}
}
