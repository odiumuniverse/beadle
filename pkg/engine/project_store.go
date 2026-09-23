package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/memory"
	proj "github.com/odiumuniverse/beadle/pkg/project"
	"github.com/odiumuniverse/beadle/pkg/state"
)

const policyFileName = "policy.json"

func (e *Engine) projectIdentity() proj.Identity {
	return proj.Resolve(e.cwd)
}

func (e *Engine) projectPolicy() (proj.Policy, error) {
	if e.policyLoaded {
		return e.policy, nil
	}

	policy, err := e.loadPolicy(e.projectIdentity().ID)
	if err != nil {
		return proj.Policy{}, err
	}

	e.policy, e.policyLoaded = policy, true

	return policy, nil
}

func (e *Engine) loadPolicy(id string) (proj.Policy, error) {
	path := e.policyPath(id)

	data, present, err := readOptional(path)
	if err != nil || !present {
		return proj.Policy{}, err
	}

	policy, err := proj.ParsePolicy(data)
	if err != nil {
		return proj.Policy{}, fmt.Errorf("%s: %w", path, err)
	}

	return policy, nil
}

func (e *Engine) savePolicy(id string, policy proj.Policy) error {
	policy.Updated = e.now().UTC()

	data, err := policy.Marshal()
	if err != nil {
		return fmt.Errorf("%s: %w", e.policyPath(id), err)
	}

	return writeVaultFile(e.policyPath(id), data)
}

func (e *Engine) policyPath(id string) string {
	return filepath.Join(e.vault.ProjectsDir(), id, policyFileName)
}

func (e *Engine) loadProjects() (kind.Items, error) {
	root := e.vault.ProjectsDir()

	ids, err := projectIDs(root)
	if err != nil {
		return nil, err
	}

	items := kind.Items{}

	for _, id := range ids {
		files, err := loadProjectFiles(root, id)
		if err != nil {
			return nil, err
		}

		maps.Copy(items, files)
	}

	return items, nil
}

func (e *Engine) saveProjects(items kind.Items) error {
	root := e.vault.ProjectsDir()
	want := map[string]map[string][]byte{}

	for key, data := range items {
		id, rel, ok := strings.Cut(key, "/")
		if !ok || !validProjectID(id) || !validProjectRel(rel) || rel == policyFileName {
			return fmt.Errorf("invalid project canon key %q", key)
		}

		if want[id] == nil {
			want[id] = map[string][]byte{}
		}

		want[id][rel] = data
	}

	ids, err := projectIDs(root)
	if err != nil {
		return err
	}

	for id := range want {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}

	for _, id := range slices.Sorted(slices.Values(ids)) {
		if err := e.saveProjectFiles(root, id, want[id]); err != nil {
			return err
		}
	}

	return nil
}

func (e *Engine) saveProjectFiles(root, id string, want map[string][]byte) error {
	dir := filepath.Join(root, id)

	current, err := loadProjectFiles(root, id)
	if err != nil {
		return err
	}

	for _, rel := range slices.Sorted(maps.Keys(want)) {
		key := id + "/" + rel

		data, present := current[key]
		if present && bytes.Equal(data, want[rel]) {
			continue
		}

		path := filepath.Join(dir, filepath.FromSlash(rel))

		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return fmt.Errorf("create project canon directory: %w", err)
		}

		if err := fsutil.WriteFileAtomic(path, want[rel], 0o600); err != nil {
			return fmt.Errorf("write project canon %s: %w", path, err)
		}
	}

	for _, key := range slices.Sorted(maps.Keys(current)) {
		rel := strings.TrimPrefix(key, id+"/")

		if _, keep := want[rel]; keep {
			continue
		}

		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove project canon %s: %w", path, err)
		}
	}

	return pruneEmptyDirs(dir)
}

func projectIDs(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("read project canon %s: %w", root, err)
	}

	var ids []string

	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&fs.ModeSymlink != 0 || !validProjectID(entry.Name()) {
			continue
		}

		ids = append(ids, entry.Name())
	}

	return ids, nil
}

func loadProjectFiles(root, id string) (kind.Items, error) {
	dir := filepath.Join(root, id)
	items := kind.Items{}

	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}

		if rel == "." || entry.IsDir() {
			return nil
		}

		if !entry.Type().IsRegular() || !validProjectRel(rel) || rel == policyFileName {
			return nil
		}

		data, err := os.ReadFile(path) //nolint:gosec // G304: paths live under the vault projects directory
		if err != nil {
			return fmt.Errorf("read project canon %s: %w", path, err)
		}

		items[id+"/"+filepath.ToSlash(rel)] = data

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read project canon %s: %w", dir, err)
	}

	return items, nil
}

func pruneEmptyDirs(root string) error {
	var dirs []string

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() && path != root {
			dirs = append(dirs, path)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("read project canon %s: %w", root, err)
	}

	slices.Reverse(dirs)

	for _, dir := range dirs {
		if err := os.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) && !isNotEmpty(err) {
			return fmt.Errorf("remove empty project canon directory %s: %w", dir, err)
		}
	}

	if err := os.Remove(root); err != nil && !errors.Is(err, fs.ErrNotExist) && !isNotEmpty(err) {
		return fmt.Errorf("remove empty project canon directory %s: %w", root, err)
	}

	return nil
}

func isNotEmpty(err error) bool {
	return errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST)
}

func validProjectID(id string) bool {
	return memory.ValidSlug(id)
}

func validProjectRel(rel string) bool {
	return rel != "" && filepath.IsLocal(rel)
}

// rootRelativeRel reports whether rel belongs to a root-relative project
// surface and to no cwd-relative one.
func (e *Engine) rootRelativeRel(rel string) bool {
	rootRelative, cwdRelative := false, false

	for _, a := range e.agents {
		for _, surface := range a.SurfacesOf(kind.Projects) {
			file, ok := surface.(agent.ProjectFile)
			if !ok || file.ProjectRel() != rel {
				continue
			}

			if _, isRoot := surface.(agent.ProjectRootRel); isRoot {
				rootRelative = true
			} else {
				cwdRelative = true
			}
		}
	}

	return rootRelative && !cwdRelative
}

func (e *Engine) projectPublishable(rel string) (bool, error) {
	identity := e.projectIdentity()
	if identity.Slugs {
		return false, nil
	}

	ignored, err := gitIgnored(identity.Root, e.projectRelFromRoot(rel))
	if err != nil {
		return false, err
	}

	return ignored, nil
}

func (e *Engine) projectRelFromRoot(rel string) string {
	identity := e.projectIdentity()
	if identity.Root == "" {
		return rel
	}

	// A rel that only root-relative surfaces claim (the DSH instruction chain)
	// already points from the checkout root; prefixing it again would look up
	// the wrong file. With a cwd-relative surface on the same rel, the
	// cwd-relative interpretation wins: that surface writes at cwd.
	if e.rootRelativeRel(rel) {
		return rel
	}

	if prefix, err := gitOutput(e.cwd, "rev-parse", "--show-prefix"); err == nil {
		return path.Join(strings.TrimSuffix(prefix, "/"), rel)
	}

	abs := filepath.Join(e.cwd, filepath.FromSlash(rel))

	if resolved, err := filepath.EvalSymlinks(e.cwd); err == nil {
		abs = filepath.Join(resolved, filepath.FromSlash(rel))
	}

	if fromRoot, err := filepath.Rel(identity.Root, abs); err == nil && filepath.IsLocal(fromRoot) {
		return filepath.ToSlash(fromRoot)
	}

	return rel
}

func gitOutput(dir string, args ...string) (string, error) {
	all := append([]string{"-C", dir}, args...)

	out, err := exec.CommandContext(context.Background(), "git", all...).Output() //nolint:gosec // G204: fixed git subcommands only
	if err != nil {
		return "", err
	}

	return strings.TrimRight(string(out), "\n"), nil
}

func gitIgnored(root, rel string) (bool, error) {
	if rel == "" {
		return false, nil
	}

	cmd := exec.CommandContext(context.Background(), "git", "-C", root, "check-ignore", "-q", "--", rel) //nolint:gosec // G204: fixed git subcommand only

	err := cmd.Run()
	switch {
	case err == nil:
		return true, nil
	case exitCode(err) == 1:
		return false, nil
	default:
		return false, fmt.Errorf("git check-ignore %s: %w", rel, err)
	}
}

func exitCode(err error) int {
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		return exit.ExitCode()
	}

	return -1
}

type ProjectFileStatus struct {
	Rel          string `json:"rel"`
	Enabled      bool   `json:"enabled"`
	AllowSecrets bool   `json:"allowSecrets,omitempty"`
	Publishable  bool   `json:"publishable"`
	Present      bool   `json:"present"`
}

type ProjectStatus struct {
	ID     string              `json:"id"`
	Root   string              `json:"root,omitempty"`
	Remote string              `json:"remote,omitempty"`
	Slugs  bool                `json:"slugs"`
	Files  []ProjectFileStatus `json:"files"`
}

type ProjectOptions struct {
	AllowSecrets bool
	KeepSecrets  bool
	Servers      []string
}

func (e *Engine) ProjectStatus(ctx context.Context) (ProjectStatus, error) {
	release, err := e.lock(ctx)
	if err != nil {
		return ProjectStatus{}, err
	}

	defer release()

	return e.projectStatus()
}

func (e *Engine) projectStatus() (ProjectStatus, error) {
	identity := e.projectIdentity()

	policy, err := e.projectPolicy()
	if err != nil {
		return ProjectStatus{}, err
	}

	status := ProjectStatus{ID: identity.ID, Root: identity.Root, Remote: identity.Remote, Slugs: identity.Slugs}

	for _, rel := range e.projectRels() {
		file, _ := policy.File(rel)

		publishable, err := e.projectPublishable(rel)
		if err != nil {
			publishable = false
		}

		status.Files = append(status.Files, ProjectFileStatus{
			Rel:          rel,
			Enabled:      file.Enabled,
			AllowSecrets: file.AllowSecrets,
			Publishable:  file.AllowSecrets || publishable,
			Present:      e.projectRelPresent(rel),
		})
	}

	return status, nil
}

// ProjectEnableDetected enables every present project file of the known set,
// keeping the secret policy untouched: secrets stay out unless the user opted
// in per file (`beadle project enable <file> --allow-secrets`). It is the
// init-time default for a git checkout; a plain directory is left alone, and
// no file is created for a rel that is not on disk.
func (e *Engine) ProjectEnableDetected(ctx context.Context) ([]string, error) {
	release, err := e.lock(ctx)
	if err != nil {
		return nil, err
	}

	defer release()

	if e.projectIdentity().Slugs {
		return nil, nil
	}

	identity := e.projectIdentity()

	policy, err := e.loadPolicy(identity.ID)
	if err != nil {
		return nil, err
	}

	var enabled []string

	for _, rel := range e.projectRels() {
		if !e.projectRelPresent(rel) {
			continue
		}

		if file, ok := policy.File(rel); ok && file.Enabled {
			continue
		}

		policy = policy.With(rel, true, false)
		enabled = append(enabled, rel)
	}

	if len(enabled) == 0 {
		return nil, nil
	}

	if err := e.savePolicy(identity.ID, policy); err != nil {
		return nil, err
	}

	e.policyLoaded = false

	return enabled, nil
}

func (e *Engine) ProjectEnable(ctx context.Context, rel string, opts ProjectOptions) (ProjectStatus, error) {
	release, err := e.lock(ctx)
	if err != nil {
		return ProjectStatus{}, err
	}

	defer release()

	if !slices.Contains(e.projectRels(), rel) {
		return ProjectStatus{}, unknownProjectRel(rel, e.projectRels())
	}

	identity := e.projectIdentity()

	policy, err := e.loadPolicy(identity.ID)
	if err != nil {
		return ProjectStatus{}, err
	}

	allowSecrets := opts.AllowSecrets

	if opts.KeepSecrets {
		file, _ := policy.File(rel)
		allowSecrets = file.AllowSecrets
	}

	policy = policy.With(rel, true, allowSecrets)

	if len(opts.Servers) > 0 {
		policy.Servers = slices.Clone(opts.Servers)
	}

	if err := e.ensureProjectSkeleton(ctx, rel); err != nil {
		return ProjectStatus{}, err
	}

	if err := e.savePolicy(identity.ID, policy); err != nil {
		return ProjectStatus{}, err
	}

	e.policyLoaded = false

	return e.projectStatus()
}

func (e *Engine) ProjectDisable(ctx context.Context, rel string) (ProjectStatus, error) {
	release, err := e.lock(ctx)
	if err != nil {
		return ProjectStatus{}, err
	}

	defer release()

	if !slices.Contains(e.projectRels(), rel) {
		return ProjectStatus{}, unknownProjectRel(rel, e.projectRels())
	}

	identity := e.projectIdentity()

	policy, err := e.loadPolicy(identity.ID)
	if err != nil {
		return ProjectStatus{}, err
	}

	if err := e.savePolicy(identity.ID, policy.With(rel, false, false)); err != nil {
		return ProjectStatus{}, err
	}

	e.policyLoaded = false

	return e.projectStatus()
}

func (e *Engine) ProjectForget(ctx context.Context, rel string) (*Report, error) {
	release, err := e.lock(ctx)
	if err != nil {
		return nil, err
	}

	defer release()

	if !slices.Contains(e.projectRels(), rel) {
		return nil, unknownProjectRel(rel, e.projectRels())
	}

	id := e.projectIdentity().ID

	if err := e.dropProjectCanon(id, rel); err != nil {
		return nil, err
	}

	if err := e.removeProjectFiles(rel); err != nil {
		return nil, err
	}

	if err := e.forgetProjectState(id, rel); err != nil {
		return nil, err
	}

	policy, err := e.loadPolicy(id)
	if err != nil {
		return nil, err
	}

	if err := e.savePolicy(id, policy.Without(rel)); err != nil {
		return nil, err
	}

	e.policyLoaded = false

	return e.sync(ctx, SyncOptions{})
}

func (e *Engine) removeProjectFiles(rel string) error {
	for _, surface := range e.projectSurfacesByRel(rel) {
		remover, ok := surface.(agent.ProjectRemover)
		if !ok {
			continue
		}

		if err := remover.Remove(); err != nil {
			return err
		}
	}

	return nil
}

func (e *Engine) dropProjectCanon(id, rel string) error {
	items, err := e.loadProjects()
	if err != nil {
		return err
	}

	for key := range items {
		if projectKeyMatches(key, id, rel) {
			delete(items, key)
		}
	}

	return e.saveProjects(items)
}

func (e *Engine) forgetProjectState(id, rel string) error {
	st, err := state.Load(e.vault.StatePath())
	if err != nil {
		return err
	}

	for _, a := range e.agents {
		base, ok := st.Base(kind.Projects, a.ID)
		if !ok {
			continue
		}

		for key := range base {
			if projectKeyMatches(key, id, rel) {
				delete(base, key)
			}
		}

		st.SetBase(kind.Projects, a.ID, base)
	}

	for _, c := range st.OpenConflicts() {
		if c.Kind == kind.Projects && projectKeyMatches(c.Key, id, rel) {
			st.RemoveConflict(c.ID())
		}
	}

	for _, surface := range e.projectSurfacesByRel(rel) {
		path := surface.Path()
		delete(st.Renders, path)
		delete(st.Drift, path)
	}

	return st.Save(e.vault.StatePath())
}

func (e *Engine) projectSurfacesByRel(rel string) []agent.Surface {
	var out []agent.Surface

	for _, a := range e.agents {
		for _, surface := range a.SurfacesOf(kind.Projects) {
			file, ok := surface.(agent.ProjectFile)
			if !ok || file.ProjectRel() != rel {
				continue
			}

			out = append(out, surface)
		}
	}

	return out
}

func projectKeyMatches(key, id, rel string) bool {
	prefix := id + "/" + rel

	return key == prefix || strings.HasPrefix(key, prefix+"/")
}

func (e *Engine) projectRels() []string {
	seen := map[string]struct{}{}

	var out []string

	for _, a := range e.agents {
		for _, surface := range a.SurfacesOf(kind.Projects) {
			file, ok := surface.(agent.ProjectFile)
			if !ok {
				continue
			}

			if _, exists := seen[file.ProjectRel()]; exists {
				continue
			}

			seen[file.ProjectRel()] = struct{}{}

			out = append(out, file.ProjectRel())
		}
	}

	slices.Sort(out)

	return out
}

func (e *Engine) projectRelPresent(rel string) bool {
	base := e.cwd

	// A root-relative rel (the DSH instruction chain) points from the checkout
	// root; with a cwd-relative surface on the same rel the cwd wins, like in
	// projectRelFromRoot. A path-slug identity has no root: fall back to cwd.
	if root := e.projectIdentity().Root; root != "" && e.rootRelativeRel(rel) {
		base = root
	}

	_, err := os.Stat(filepath.Join(base, filepath.FromSlash(rel)))

	return err == nil
}

func (e *Engine) ensureProjectSkeleton(ctx context.Context, rel string) error {
	if e.projectRelPresent(rel) {
		return nil
	}

	for _, a := range e.agents {
		for _, surface := range a.SurfacesOf(kind.Projects) {
			file, ok := surface.(agent.ProjectFile)
			if !ok || file.ProjectRel() != rel {
				continue
			}

			skeleton, ok := surface.(agent.ProjectSkeleton)
			if !ok {
				return nil
			}

			return surface.Write(ctx, kind.Items{e.projectIdentity().ID + "/" + rel: skeleton.Skeleton()})
		}
	}

	return nil
}

func unknownProjectRel(rel string, known []string) error {
	return fmt.Errorf("unknown project file %q (known: %s)", rel, strings.Join(known, ", "))
}
