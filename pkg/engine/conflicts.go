package engine

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pmezard/go-difflib/difflib"
	"github.com/tailscale/hujson"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/cas"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/mcp"
	"github.com/odiumuniverse/beadle/pkg/permission"
	"github.com/odiumuniverse/beadle/pkg/state"
)

var conflictFileName = regexp.MustCompile(`^(rules|mcp|skills|permissions|memory|projects)-[a-z0-9-]+-[0-9a-f]{8}\.[A-Za-z0-9]+$`)

var extPattern = regexp.MustCompile(`^\.[A-Za-z0-9]{1,8}$`)

type Take string

const (
	TakeVault   Take = "vault"
	TakeAgent   Take = "agent"
	TakeFile    Take = "file"
	TakeContent Take = "content"
)

func ParseTake(name string) (Take, error) {
	switch take := Take(strings.ToLower(name)); take {
	case TakeVault, TakeAgent, TakeFile, TakeContent:
		return take, nil
	default:
		return "", fmt.Errorf("unknown resolution %q (expected vault, agent or file)", name)
	}
}

type Resolution struct {
	Take        Take
	Content     []byte
	ExpectBase  string
	ExpectVault string
	ExpectAgent string
	AllowRisky  bool
}

// ConflictView is the stable, secret-redacted JSON projection of one conflict.
type ConflictView struct {
	ID        string            `json:"id"`
	Kind      kind.ID           `json:"kind"`
	Agent     string            `json:"agent"`
	Key       string            `json:"key"`
	VaultKey  string            `json:"vault_key,omitempty"`
	Reason    string            `json:"reason"`
	Since     time.Time         `json:"since"`
	Base      cas.Hash          `json:"base"`
	Vault     cas.Hash          `json:"vault"`
	AgentHash cas.Hash          `json:"agent_hash"`
	File      string            `json:"file"`
	Binary    bool              `json:"binary"`
	Values    map[string]string `json:"values,omitempty"`
	Sizes     map[string]int    `json:"sizes,omitempty"`
	Patch     string            `json:"patch"`
}

// refusalError is an expected, reportable refusal (not a programming error).
type refusalError struct {
	code    string
	message string
}

const valueBase = "base"

func (r refusalError) Error() string {
	return r.code + ": " + r.message
}

func refusal(code, message string) error {
	return refusalError{code: code, message: message}
}

// ConflictViews lists every open conflict in a stable, secret-redacted form.
func (e *Engine) ConflictViews() ([]ConflictView, error) {
	st, err := state.Load(e.vault.StatePath())
	if err != nil {
		return nil, err
	}

	conflicts := st.OpenConflicts()

	slices.SortFunc(conflicts, func(a, b state.Conflict) int { return cmp.Compare(a.ID(), b.ID()) })

	views := make([]ConflictView, 0, len(conflicts))

	for _, c := range conflicts {
		view, err := e.conflictView(c)
		if err != nil {
			return nil, err
		}

		views = append(views, view)
	}

	return views, nil
}

func (e *Engine) conflictView(c state.Conflict) (ConflictView, error) {
	base, vaultValue, local, err := e.ConflictValues(c)
	if err != nil {
		return ConflictView{}, err
	}

	view := ConflictView{
		ID: c.ID(), Kind: c.Kind, Agent: c.Agent, Key: c.Key, VaultKey: c.VaultKey,
		Reason: c.Reason, Since: c.Since, Base: c.Base, Vault: c.Vault, AgentHash: c.Local,
	}

	if file, err := e.ConflictFile(c); err == nil {
		view.File = file
	}

	if !kind.IsText(base) || !kind.IsText(vaultValue) || !kind.IsText(local) {
		view.Binary = true
		view.Sizes = map[string]int{valueBase: len(base), string(TakeVault): len(vaultValue), string(TakeAgent): len(local)}

		return view, nil
	}

	view.Values = map[string]string{
		valueBase:         readableValue(c.Kind, []byte(e.redact(string(base)))),
		string(TakeVault): readableValue(c.Kind, []byte(e.redact(string(vaultValue)))),
		string(TakeAgent): readableValue(c.Kind, []byte(e.redact(string(local)))),
	}
	view.Patch = conflictPatch([]byte(e.redact(string(vaultValue))), []byte(e.redact(string(local))))

	return view, nil
}

func readableValue(k kind.ID, data []byte) string {
	if data == nil {
		return ""
	}

	if k == kind.MCP {
		var out bytes.Buffer

		if err := json.Indent(&out, data, "", "  "); err == nil {
			return out.String() + "\n"
		}
	}

	return string(data)
}

func conflictPatch(vaultValue, agentValue []byte) string {
	if len(vaultValue) == 0 && len(agentValue) == 0 {
		return ""
	}

	diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(string(vaultValue)),
		B:        difflib.SplitLines(string(agentValue)),
		FromFile: "vault",
		ToFile:   "agent",
		Context:  3,
	})
	if err != nil {
		return ""
	}

	return diff
}

func (e *Engine) redact(text string) string {
	if text == "" || e.secrets == nil {
		return text
	}

	names := slices.SortedFunc(slices.Values(e.secrets.Names()), func(a, b string) int {
		av, _ := e.secrets.Get(a)
		bv, _ := e.secrets.Get(b)

		return cmp.Or(cmp.Compare(len(bv), len(av)), cmp.Compare(a, b))
	})

	for _, name := range names {
		value, ok := e.secrets.Get(name)
		if !ok || value == "" {
			continue
		}

		text = strings.ReplaceAll(text, value, "⟨secret:"+name+"⟩")
	}

	return text
}

func (e *Engine) Conflicts() ([]state.Conflict, error) {
	st, err := state.Load(e.vault.StatePath())
	if err != nil {
		return nil, err
	}

	return st.OpenConflicts(), nil
}

func (e *Engine) ConflictValues(c state.Conflict) ([]byte, []byte, []byte, error) {
	base, err := e.blob(c.Base)
	if err != nil {
		return nil, nil, nil, err
	}

	vaultValue, err := e.blob(c.Vault)
	if err != nil {
		return nil, nil, nil, err
	}

	local, err := e.blob(c.Local)
	if err != nil {
		return nil, nil, nil, err
	}

	return base, vaultValue, local, nil
}

func (e *Engine) ConflictFile(c state.Conflict) (string, error) {
	name, _, err := e.conflictDocument(c)
	if err != nil {
		return "", err
	}

	return filepath.Join(e.vault.ConflictsDir(), name), nil
}

func (e *Engine) Resolve(ctx context.Context, ids []string, res Resolution) (*Report, error) {
	release, err := e.lock(ctx)
	if err != nil {
		return nil, err
	}

	defer release()

	st, err := state.Load(e.vault.StatePath())
	if err != nil {
		return nil, err
	}

	now := e.now().UTC()

	var resolved []string

	var refusals []state.Refusal

	for _, id := range ids {
		c, err := st.Conflict(id)
		if err != nil {
			refusals = append(refusals, e.recordUnknownRefusal(st, now, id, refusalCode(err.Error()), err.Error()))

			continue
		}

		err = e.resolveConflict(st, c, res)
		if err != nil {
			if refused, ok := errors.AsType[refusalError](err); ok {
				refusals = append(refusals, e.recordRefusal(st, now, c, refused.code, refused.message))

				continue
			}

			if saveErr := st.Save(e.vault.StatePath()); saveErr != nil {
				return nil, saveErr
			}

			return nil, fmt.Errorf("resolve %s: %w", c.ID(), err)
		}

		resolved = append(resolved, c.ID())
	}

	if err := st.Save(e.vault.StatePath()); err != nil {
		return nil, err
	}

	report, err := e.sync(ctx, SyncOptions{})
	if err != nil {
		return nil, err
	}

	report.Resolved = resolved
	report.Refusals = refusals

	return report, nil
}

func (e *Engine) recordRefusal(st *state.State, at time.Time, c state.Conflict, code, message string) state.Refusal {
	refusal := state.Refusal{
		At: at, ID: c.ID(), Kind: c.Kind, Agent: c.Agent, Key: c.TargetKey(), Code: code, Message: e.redact(message),
	}

	st.AddRefusal(refusal)

	return refusal
}

func (e *Engine) recordUnknownRefusal(st *state.State, at time.Time, id, code, message string) state.Refusal {
	refusal := state.Refusal{At: at, ID: id, Code: code, Message: e.redact(message)}

	st.AddRefusal(refusal)

	return refusal
}

func refusalCode(message string) string {
	if strings.Contains(message, "ambiguous") {
		return state.RefusalAmbiguous
	}

	return state.RefusalUnknownConflict
}

func (e *Engine) resolveConflict(st *state.State, c state.Conflict, res Resolution) error {
	spec, ok := kind.Lookup(c.Kind)
	if !ok {
		return fmt.Errorf("unknown kind %q", c.Kind)
	}

	local, err := e.blob(c.Local)
	if err != nil {
		return err
	}

	if err := e.applyResolution(spec, c, res, local); err != nil {
		return err
	}

	if err := e.setBaseKey(st, c, local); err != nil {
		return err
	}

	st.RemoveConflict(c.ID())

	if file, err := e.ConflictFile(c); err == nil {
		_ = os.Remove(file)
	}

	return nil
}

func (e *Engine) applyResolution(spec kind.Spec, c state.Conflict, res Resolution, local []byte) error {
	switch res.Take {
	case TakeVault:
		return nil
	case TakeAgent:
		return e.takeAgent(spec, c, local)
	case TakeFile:
		content, err := e.readConflictFile(c)
		if err != nil {
			return err
		}

		return e.takeContent(c, content)
	case TakeContent:
		return e.takeResolvedContent(c, res)
	default:
		return fmt.Errorf("unknown resolution %q", res.Take)
	}
}

func (e *Engine) takeResolvedContent(c state.Conflict, res Resolution) error {
	if c.Base != cas.Hash(res.ExpectBase) || c.Vault != cas.Hash(res.ExpectVault) || c.Local != cas.Hash(res.ExpectAgent) {
		return refusal(state.RefusalStaleConflict, "the conflict changed since it was read; re-read beadle conflicts --json and retry")
	}

	content := res.Content

	if _, err := normalizeContent(c.Kind, content); err != nil {
		return refusal(state.RefusalInvalidContent, err.Error())
	}

	if !res.AllowRisky {
		risky, err := e.riskyContent(c, content)
		if err != nil {
			return err
		}

		if risky {
			return refusal(state.RefusalRiskyChange, "the content changes an MCP command/url or a permission rule; re-run with --allow-risky")
		}
	}

	return e.takeContent(c, content)
}

func (e *Engine) riskyContent(c state.Conflict, content []byte) (bool, error) {
	switch c.Kind {
	case kind.Permissions:
		return true, nil
	case kind.MCP:
		vaultItems, _, err := e.loadVault(kind.MCP)
		if err != nil {
			return false, err
		}

		current := vaultItems[c.TargetKey()]
		if current == nil {
			return true, nil
		}

		normalized, err := normalizeContent(kind.MCP, content)
		if err != nil {
			return false, err
		}

		old, err := mcp.Decode(current)
		if err != nil {
			return false, err
		}

		replacement, err := mcp.Decode(normalized)
		if err != nil {
			return false, err
		}

		return !slices.Equal(old.Command, replacement.Command) || old.URL != replacement.URL, nil
	default:
		return false, nil
	}
}

func (e *Engine) takeAgent(spec kind.Spec, c state.Conflict, local []byte) error {
	vaultItems, _, err := e.loadVault(c.Kind)
	if err != nil {
		return err
	}

	proj := projection{items: kind.Items{}, vkeys: map[string][]string{}, hidden: map[string]bool{}}

	if surface := e.surfaceOf(c.Agent, c.Kind, c.Key); surface != nil {
		proj = project(vaultItems, surface)
	}

	if c.VaultKey != "" {
		proj.vkeys[c.Key] = []string{c.VaultKey}
	}

	adopt(spec, proj, vaultItems, c.Agent, c.Key, local, &KindReport{})

	return e.saveVault(c.Kind, vaultItems)
}

func (e *Engine) takeContent(c state.Conflict, content []byte) error {
	normalized, err := normalizeContent(c.Kind, content)
	if err != nil {
		return err
	}

	vaultItems, _, err := e.loadVault(c.Kind)
	if err != nil {
		return err
	}

	vaultItems[c.TargetKey()] = normalized

	return e.saveVault(c.Kind, vaultItems)
}

func (e *Engine) readConflictFile(c state.Conflict) ([]byte, error) {
	file, err := e.ConflictFile(c)
	if err != nil {
		return nil, err
	}

	if filepath.Ext(file) == ".json" {
		return nil, fmt.Errorf("%s is a summary of a structured conflict: resolve it with --take vault or --take agent", file)
	}

	data, err := os.ReadFile(file) //nolint:gosec // G304: the file lives in the vault conflicts directory
	if err != nil {
		return nil, fmt.Errorf("read conflict file: %w", err)
	}

	return data, nil
}

func normalizeContent(k kind.ID, content []byte) ([]byte, error) {
	switch k {
	case kind.MCP:
		standard, err := hujson.Standardize(content)
		if err != nil {
			return nil, errors.New("an MCP server must be a JSON object")
		}

		var object map[string]any

		if err := json.Unmarshal(standard, &object); err != nil || object == nil {
			return nil, errors.New("an MCP server must be a JSON object")
		}

		server, err := mcp.Decode(standard)
		if err != nil || !server.Valid() {
			return nil, errors.New("an MCP server needs a command or a url")
		}

		return kind.CanonicalJSON(standard)
	case kind.Permissions:
		effect := strings.TrimSpace(string(content))
		if !permission.ValidEffect(effect) {
			return nil, fmt.Errorf("a permission rule takes allow, ask or deny, not %q", effect)
		}

		return []byte(effect), nil
	default:
		if kind.HasMarkers(content) {
			return nil, errors.New("the content still contains conflict markers")
		}

		if !utf8.Valid(content) {
			return nil, errors.New("the content is not valid UTF-8 text")
		}

		return content, nil
	}
}

func (e *Engine) setBaseKey(st *state.State, c state.Conflict, local []byte) error {
	current, _ := st.Base(c.Kind, c.Agent)

	base := maps.Clone(current)
	if base == nil {
		base = state.Base{}
	}

	if local == nil {
		delete(base, c.Key)
	} else {
		hash, err := e.store.Put(local)
		if err != nil {
			return err
		}

		base[c.Key] = hash
	}

	st.SetBase(c.Kind, c.Agent, base)

	return nil
}

func (e *Engine) surfaceOf(agentID string, k kind.ID, key string) agent.Surface {
	a := agent.ByID(e.agents, agentID)
	if a == nil {
		return nil
	}

	surfaces := a.SurfacesOf(k)

	for _, surface := range surfaces {
		projector, ok := surface.(agent.Projector)
		if !ok {
			continue
		}

		if _, _, visible := projector.Project(key, nil); visible {
			return surface
		}
	}

	if len(surfaces) > 0 {
		return surfaces[0]
	}

	return nil
}

func (e *Engine) writeConflictFiles(st *state.State) error {
	dir := e.vault.ConflictsDir()

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create conflicts directory: %w", err)
	}

	wanted := map[string][]byte{}

	for _, c := range st.OpenConflicts() {
		name, content, err := e.conflictDocument(c)
		if err != nil {
			return err
		}

		wanted[name] = content
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read conflicts directory: %w", err)
	}

	for _, entry := range entries {
		if _, keep := wanted[entry.Name()]; keep || !conflictFileName.MatchString(entry.Name()) {
			continue
		}

		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			return fmt.Errorf("remove stale conflict file: %w", err)
		}
	}

	for _, name := range slices.Sorted(maps.Keys(wanted)) {
		file := filepath.Join(dir, name)
		if fsutil.Exists(file) {
			continue
		}

		if err := fsutil.WriteFileAtomic(file, wanted[name], 0o600); err != nil {
			return fmt.Errorf("write conflict file: %w", err)
		}
	}

	return nil
}

func (e *Engine) conflictDocument(c state.Conflict) (string, []byte, error) {
	base, vaultValue, local, err := e.ConflictValues(c)
	if err != nil {
		return "", nil, err
	}

	prefix := fmt.Sprintf("%s-%s-%s", c.Kind, c.Agent, c.ID())

	if (c.Kind == kind.Rules || c.Kind == kind.Skills || c.Kind == kind.Memory || c.Kind == kind.Projects) && kind.IsText(base) && kind.IsText(vaultValue) && kind.IsText(local) {
		ext := ".md"

		if c.Kind == kind.Skills {
			ext = ".txt"
			if candidate := path.Ext(c.Key); extPattern.MatchString(candidate) {
				ext = candidate
			}
		}

		return prefix + ext, kind.ConflictDocument(base, vaultValue, local, "agent:"+c.Agent), nil
	}

	summary := map[string]any{
		"kind":        c.Kind,
		"agent":       c.Agent,
		"key":         c.Key,
		"vault_key":   c.TargetKey(),
		"reason":      c.Reason,
		"base":        display(c.Kind, base),
		"vault":       display(c.Kind, vaultValue),
		"agent_value": display(c.Kind, local),
		"resolve":     "beadle resolve " + c.ID() + " --take vault|agent",
	}

	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return "", nil, fmt.Errorf("encode conflict summary: %w", err)
	}

	return prefix + ".json", append(data, '\n'), nil
}

func display(k kind.ID, data []byte) any {
	if data == nil {
		return nil
	}

	if k == kind.MCP {
		var decoded any

		if err := json.Unmarshal(data, &decoded); err == nil {
			return decoded
		}
	}

	if utf8.Valid(data) {
		return string(data)
	}

	return fmt.Sprintf("<%d bytes of binary data>", len(data))
}
