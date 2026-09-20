package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/merge"
	"github.com/odiumuniverse/beadle/pkg/secret"
	"github.com/odiumuniverse/beadle/pkg/state"
)

const (
	MergetoolActive  = "active"
	MergetoolAborted = "aborted"

	mergetoolRefPrefix = "refs/beadle/mergetool/"

	mergetoolUnsupported = "mergetool-unsupported"
	mergetoolIsActive    = "mergetool-active"
	mergetoolNotActive   = "mergetool-not-active"
)

var mergetoolRunner secret.Runner = secret.ExecRunner{}

// MergetoolResult describes one materialized or aborted audit merge.
type MergetoolResult struct {
	ID    string   `json:"id"`
	Path  string   `json:"path"`
	Refs  []string `json:"refs"`
	State string   `json:"state"`
}

type mergetoolFile struct {
	ID        string   `json:"id"`
	Path      string   `json:"path"`
	Refs      []string `json:"refs"`
	Index     []string `json:"index,omitempty"`
	VaultBlob string   `json:"vault_blob,omitempty"`
}

// Mergetool materializes one text conflict as a real git merge state in the
// vault working tree: index stages 1/2/3, MERGE_HEAD, conflict markers and
// audit refs per side. It never resolves anything by itself.
func (e *Engine) Mergetool(ctx context.Context, id string) (MergetoolResult, []string, error) {
	release, err := e.lock(ctx)
	if err != nil {
		return MergetoolResult{}, nil, err
	}

	defer release()

	if err := e.mergetoolRepo(); err != nil {
		return MergetoolResult{}, nil, err
	}

	active, err := e.mergetoolActive()
	if err != nil {
		return MergetoolResult{}, nil, err
	}

	if active {
		return MergetoolResult{}, nil, e.mergetoolActiveRefusal()
	}

	c, rel, base, vaultValue, local, err := e.mergetoolConflict(id)
	if err != nil {
		return MergetoolResult{}, nil, err
	}

	result, err := e.mergetoolApply(c, rel, base, vaultValue, local)
	if err != nil {
		return MergetoolResult{}, nil, err
	}

	return result, e.mergetoolWarnings(rel), nil
}

// MergetoolAbort removes the active mergetool merge state, restoring the index
// and the working tree of the target path. Audit refs stay forever.
func (e *Engine) MergetoolAbort(ctx context.Context, id string) (MergetoolResult, error) {
	release, err := e.lock(ctx)
	if err != nil {
		return MergetoolResult{}, err
	}

	defer release()

	if err := e.mergetoolRepo(); err != nil {
		return MergetoolResult{}, err
	}

	file, found, err := e.loadMergetoolFile()
	if err != nil {
		return MergetoolResult{}, err
	}

	if !found || id != file.ID {
		return MergetoolResult{}, refusal(mergetoolNotActive, fmt.Sprintf("no active mergetool merge for %q", id))
	}

	if err := e.mergetoolClear(file, true); err != nil {
		return MergetoolResult{}, err
	}

	return MergetoolResult{
		ID:    file.ID,
		Path:  filepath.Join(e.vault.Root(), filepath.FromSlash(file.Path)),
		Refs:  file.Refs,
		State: MergetoolAborted,
	}, nil
}

func (e *Engine) mergetoolConflict(id string) (state.Conflict, string, []byte, []byte, []byte, error) {
	st, err := state.Load(e.vault.StatePath())
	if err != nil {
		return state.Conflict{}, "", nil, nil, nil, err
	}

	c, err := st.Conflict(id)
	if err != nil {
		return state.Conflict{}, "", nil, nil, nil, refusal(refusalCode(err.Error()), err.Error())
	}

	rel, err := mergetoolRel(c)
	if err != nil {
		return state.Conflict{}, "", nil, nil, nil, err
	}

	base, vaultValue, local, err := e.ConflictValues(c)
	if err != nil {
		return state.Conflict{}, "", nil, nil, nil, err
	}

	if !kind.IsText(base) || !kind.IsText(vaultValue) || !kind.IsText(local) {
		return state.Conflict{}, "", nil, nil, nil, refusal(mergetoolUnsupported,
			fmt.Sprintf("conflict %s holds binary content; resolve it with --take vault|agent", c.ID()))
	}

	return c, rel, base, vaultValue, local, nil
}

func (e *Engine) mergetoolApply(c state.Conflict, rel string, base, vaultValue, local []byte) (MergetoolResult, error) {
	root := e.vault.Root()

	index, err := mergetoolIndexLines(root, rel)
	if err != nil {
		return MergetoolResult{}, err
	}

	mode := mergetoolMode(root, rel, index)

	_, baseBlob, err := e.mergetoolSide(c, rel, mode, "base", base)
	if err != nil {
		return MergetoolResult{}, err
	}

	_, vaultBlob, err := e.mergetoolSide(c, rel, mode, "vault", vaultValue)
	if err != nil {
		return MergetoolResult{}, err
	}

	localCommit, localBlob, err := e.mergetoolSide(c, rel, mode, "local", local)
	if err != nil {
		return MergetoolResult{}, err
	}

	refs := []string{
		mergetoolRef(c.ID(), "base"),
		mergetoolRef(c.ID(), "vault"),
		mergetoolRef(c.ID(), "local"),
	}

	file := mergetoolFile{ID: c.ID(), Path: rel, Refs: refs, Index: index, VaultBlob: vaultBlob}

	if err := e.saveMergetoolFile(file); err != nil {
		return MergetoolResult{}, err
	}

	if err := mergetoolStage(root, rel, mode, []string{baseBlob, vaultBlob, localBlob}); err != nil {
		return MergetoolResult{}, err
	}

	if err := mergetoolWorktree(root, rel, base, vaultValue, local, c.ID()); err != nil {
		return MergetoolResult{}, err
	}

	if err := e.mergetoolHead(c, localCommit); err != nil {
		return MergetoolResult{}, err
	}

	return MergetoolResult{
		ID:    c.ID(),
		Path:  filepath.Join(root, filepath.FromSlash(rel)),
		Refs:  refs,
		State: MergetoolActive,
	}, nil
}

func (e *Engine) mergetoolSide(c state.Conflict, rel, mode, side string, content []byte) (string, string, error) {
	root := e.vault.Root()

	blob, tree, err := mergetoolSideTree(root, rel, mode, content)
	if err != nil {
		return "", "", err
	}

	message := fmt.Sprintf("beadle mergetool %s %s %s (%s side)", c.ID(), c.Kind, c.TargetKey(), side)

	commit, err := mergetoolGit(root, nil, "commit-tree", tree, "-m", message)
	if err != nil {
		return "", "", err
	}

	commitStr := strings.TrimSpace(string(commit))

	if err := mergetoolGitRef(root, mergetoolRef(c.ID(), side), commitStr); err != nil {
		return "", "", err
	}

	return commitStr, blob, nil
}

func mergetoolSideTree(root, rel, mode string, content []byte) (string, string, error) {
	if content == nil {
		out, err := mergetoolGit(root, nil, "mktree")
		if err != nil {
			return "", "", err
		}

		return "", strings.TrimSpace(string(out)), nil
	}

	blob, err := mergetoolBlob(root, content)
	if err != nil {
		return "", "", err
	}

	tree, err := mergetoolTree(root, rel, mode, blob)
	if err != nil {
		return "", "", err
	}

	return blob, tree, nil
}

func (e *Engine) mergetoolRepo() error {
	root, err := filepath.EvalSymlinks(e.vault.Root())
	if err != nil {
		return fmt.Errorf("resolve vault root: %w", err)
	}

	out, err := mergetoolGit(e.vault.Root(), nil, "rev-parse", "--show-toplevel")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("mergetool needs the git CLI installed: %w", err)
		}

		return fmt.Errorf("mergetool needs a git repository in %s (run git init there first): %w", e.vault.Root(), err)
	}

	top, err := filepath.EvalSymlinks(strings.TrimSpace(string(out)))
	if err != nil || top != root {
		return fmt.Errorf("vault %s is not the root of its git repository (%s)", e.vault.Root(), strings.TrimSpace(string(out)))
	}

	return nil
}

func (e *Engine) mergetoolActive() (bool, error) {
	if _, found, err := e.loadMergetoolFile(); err != nil || found {
		return found, err
	}

	gitDir, err := e.mergetoolGitDir()
	if err != nil {
		return false, err
	}

	if _, err := os.Stat(filepath.Join(gitDir, "MERGE_HEAD")); err == nil {
		return true, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("inspect MERGE_HEAD: %w", err)
	}

	return false, nil
}

func (e *Engine) mergetoolActiveRefusal() error {
	file, found, err := e.loadMergetoolFile()
	if err != nil {
		return err
	}

	if found {
		return refusal(mergetoolIsActive, fmt.Sprintf(
			"mergetool merge %s is already active; finish it or run beadle resolve %s --mergetool-abort", file.ID, file.ID))
	}

	return refusal(mergetoolIsActive, "a merge is already in progress in the vault; finish it or run git merge --abort")
}

func (e *Engine) mergetoolHead(c state.Conflict, commit string) error {
	gitDir, err := e.mergetoolGitDir()
	if err != nil {
		return err
	}

	if err := fsutil.WriteFileAtomic(filepath.Join(gitDir, "MERGE_HEAD"), []byte(commit+"\n"), 0o644); err != nil {
		return fmt.Errorf("write MERGE_HEAD: %w", err)
	}

	message := fmt.Sprintf("beadle mergetool: %s %s conflicts with %s\n", c.Kind, c.TargetKey(), c.Agent)

	if err := fsutil.WriteFileAtomic(filepath.Join(gitDir, "MERGE_MSG"), []byte(message), 0o644); err != nil {
		return fmt.Errorf("write MERGE_MSG: %w", err)
	}

	return nil
}

func (e *Engine) mergetoolGitDir() (string, error) {
	out, err := mergetoolGit(e.vault.Root(), nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(out)), nil
}

func (e *Engine) mergetoolWarnings(rel string) []string {
	out, err := mergetoolGit(e.vault.Root(), nil, "status", "--porcelain", "-z", "--untracked-files=no")
	if err != nil {
		return []string{"cannot inspect the vault working tree: " + err.Error()}
	}

	dirty := 0

	for record := range strings.SplitSeq(strings.TrimRight(string(out), "\x00"), "\x00") {
		if len(record) < 4 || record[3:] == rel {
			continue
		}

		dirty++
	}

	if dirty == 0 {
		return nil
	}

	return []string{fmt.Sprintf("vault has %d uncommitted change(s) outside %s; they are left untouched", dirty, rel)}
}

func (e *Engine) mergetoolClear(file mergetoolFile, restore bool) error {
	root := e.vault.Root()

	if err := mergetoolResetIndex(root, file); err != nil {
		return err
	}

	if restore {
		if err := mergetoolRestoreWorktree(root, file); err != nil {
			return err
		}
	}

	gitDir, err := e.mergetoolGitDir()
	if err != nil {
		return err
	}

	for _, name := range []string{"MERGE_HEAD", "MERGE_MSG"} {
		if err := os.Remove(filepath.Join(gitDir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", name, err)
		}
	}

	if err := os.Remove(e.vault.MergetoolPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove mergetool state: %w", err)
	}

	return nil
}

func (e *Engine) mergetoolAfterResolve(ids []string, restore bool) []string {
	file, found, err := e.loadMergetoolFile()
	if err != nil || !found || !slices.Contains(ids, file.ID) {
		return nil
	}

	if err := e.mergetoolClear(file, restore); err != nil {
		return []string{"mergetool cleanup: " + err.Error()}
	}

	return nil
}

func (e *Engine) mergetoolGuard() error {
	file, found, err := e.loadMergetoolFile()
	if err != nil {
		return err
	}

	if !found {
		return nil
	}

	return refusal(mergetoolIsActive, fmt.Sprintf(
		"mergetool merge %s is active; finish it or run beadle resolve %s --mergetool-abort", file.ID, file.ID))
}

func (e *Engine) saveMergetoolFile(file mergetoolFile) error {
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("encode mergetool state: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(e.vault.MergetoolPath()), 0o700); err != nil {
		return fmt.Errorf("create mergetool state directory: %w", err)
	}

	if err := fsutil.WriteFileAtomic(e.vault.MergetoolPath(), append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write mergetool state: %w", err)
	}

	return nil
}

func (e *Engine) loadMergetoolFile() (mergetoolFile, bool, error) {
	data, err := os.ReadFile(e.vault.MergetoolPath()) //nolint:gosec // G304: the path is inside the vault root
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return mergetoolFile{}, false, nil
	case err != nil:
		return mergetoolFile{}, false, fmt.Errorf("read mergetool state: %w", err)
	}

	var file mergetoolFile

	if err := json.Unmarshal(data, &file); err != nil {
		return mergetoolFile{}, false, fmt.Errorf("parse mergetool state: %w", err)
	}

	return file, true, nil
}

func mergetoolRel(c state.Conflict) (string, error) {
	var rel string

	switch c.Kind {
	case kind.Rules:
		rel = filepath.Join("rules", "base.md")
	case kind.Skills, kind.Memory, kind.Projects:
		rel = filepath.Join(string(c.Kind), filepath.FromSlash(c.TargetKey()))
	default:
		return "", refusal(mergetoolUnsupported, fmt.Sprintf(
			"%s conflicts are structured JSON and have no plain file; use --take vault|agent or --from <file>", c.Kind))
	}

	rel = filepath.ToSlash(rel)
	if !filepath.IsLocal(filepath.FromSlash(rel)) {
		return "", fmt.Errorf("conflict key %q escapes the vault", c.TargetKey())
	}

	return rel, nil
}

func mergetoolRef(id, side string) string {
	return mergetoolRefPrefix + id + "/" + side
}

func mergetoolBlob(root string, content []byte) (string, error) {
	out, err := mergetoolGit(root, content, "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(out)), nil
}

func mergetoolTree(root, rel, mode, blob string) (string, error) {
	parts := strings.Split(rel, "/")
	entry := fmt.Sprintf("%s blob %s\t%s\n", mode, blob, parts[len(parts)-1])

	out, err := mergetoolGit(root, []byte(entry), "mktree")
	if err != nil {
		return "", err
	}

	tree := strings.TrimSpace(string(out))

	for i := len(parts) - 2; i >= 0; i-- {
		entry := fmt.Sprintf("040000 tree %s\t%s\n", tree, parts[i])

		out, err = mergetoolGit(root, []byte(entry), "mktree")
		if err != nil {
			return "", err
		}

		tree = strings.TrimSpace(string(out))
	}

	return tree, nil
}

func mergetoolGitRef(root, ref, commit string) error {
	_, err := mergetoolGit(root, nil, "update-ref", ref, commit)

	return err
}

func mergetoolIndexLines(root, rel string) ([]string, error) {
	out, err := mergetoolGit(root, nil, "ls-files", "-s", "-z", "--", mergetoolLiteral(rel))
	if err != nil {
		return nil, err
	}

	trimmed := strings.TrimRight(string(out), "\x00")
	if trimmed == "" {
		return nil, nil
	}

	return strings.Split(trimmed, "\x00"), nil
}

func mergetoolMode(root, rel string, index []string) string {
	for _, line := range index {
		mode, _, ok := strings.Cut(line, " ")
		if ok && len(mode) == 6 {
			return mode
		}
	}

	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	if err == nil && info.Mode().Perm()&0o111 != 0 {
		return "100755"
	}

	return "100644"
}

func mergetoolStage(root, rel, mode string, blobs []string) error {
	lines := make([]string, 0, len(blobs))

	for i, blob := range blobs {
		if blob == "" {
			continue
		}

		lines = append(lines, fmt.Sprintf("%s %s %d\t%s", mode, blob, i+1, rel))
	}

	if len(lines) == 0 {
		return errors.New("mergetool: the conflict has no content side to stage")
	}

	return mergetoolIndexInfo(root, lines)
}

func mergetoolIndexInfo(root string, lines []string) error {
	_, err := mergetoolGit(root, []byte(strings.Join(lines, "\x00")+"\x00"), "update-index", "-z", "--index-info")

	return err
}

func mergetoolLiteral(rel string) string {
	return ":(literal)" + rel
}

func mergetoolWorktree(root, rel string, base, vaultValue, local []byte, id string) error {
	path := filepath.Join(root, filepath.FromSlash(rel))

	var content []byte

	switch {
	case vaultValue == nil && local == nil:
		return nil
	case vaultValue == nil:
		content = local
	case local == nil:
		content = vaultValue
	default:
		content = merge.Text(base, vaultValue, local, merge.TextOptions{AgentLabel: "agent/" + id}).Merged
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create mergetool directory: %w", err)
	}

	if err := fsutil.WriteFileAtomic(path, content, mergetoolPerm(path)); err != nil {
		return fmt.Errorf("write mergetool worktree file: %w", err)
	}

	return nil
}

func mergetoolResetIndex(root string, file mergetoolFile) error {
	zero := strings.Repeat("0", 40)
	remove := make([]string, 0, 3)

	for stage := 1; stage <= 3; stage++ {
		remove = append(remove, fmt.Sprintf("0 %s %d\t%s", zero, stage, file.Path))
	}

	if err := mergetoolIndexInfo(root, remove); err != nil {
		return err
	}

	if len(file.Index) == 0 {
		return nil
	}

	return mergetoolIndexInfo(root, file.Index)
}

func mergetoolRestoreWorktree(root string, file mergetoolFile) error {
	path := filepath.Join(root, filepath.FromSlash(file.Path))

	if file.VaultBlob == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove mergetool file: %w", err)
		}

		return nil
	}

	content, err := mergetoolGit(root, nil, "cat-file", "blob", file.VaultBlob)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create mergetool directory: %w", err)
	}

	if err := fsutil.WriteFileAtomic(path, content, mergetoolPerm(path)); err != nil {
		return fmt.Errorf("restore mergetool worktree file: %w", err)
	}

	return nil
}

func mergetoolPerm(path string) fs.FileMode {
	if info, err := os.Stat(path); err == nil {
		return info.Mode().Perm()
	}

	return 0o644
}

func mergetoolGit(root string, stdin []byte, args ...string) ([]byte, error) {
	all := append([]string{"-C", root, "-c", "user.name=beadle", "-c", "user.email=beadle@localhost"}, args...)

	out, code, err := mergetoolRunner.Run("git", all, stdin)
	switch {
	case err != nil:
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	case code != 0:
		return nil, fmt.Errorf("git %s: exit %d: %s", strings.Join(args, " "), code, strings.TrimSpace(string(out)))
	}

	return out, nil
}
