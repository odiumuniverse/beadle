package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/odiumuniverse/beadle/pkg/memory"
)

type Identity struct {
	ID     string
	Root   string
	Remote string
	Slugs  bool
}

const (
	hashLen    = 8
	slugMaxLen = 32
)

var (
	slugPattern   = regexp.MustCompile(`[^a-z0-9]+`)
	identityCache sync.Map
)

func Resolve(cwd string) Identity {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		abs = cwd
	}

	if cached, ok := identityCache.Load(abs); ok {
		identity, _ := cached.(Identity)

		return identity
	}

	identity := resolve(abs)
	identityCache.Store(abs, identity)

	return identity
}

func (id Identity) Publishable() bool {
	return !id.Slugs
}

func resolve(abs string) Identity {
	lines, err := gitLines(abs, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir")
	if err != nil || len(lines) != 2 {
		return slugIdentity(abs)
	}

	root, common := strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1])
	if root == "" || common == "" {
		return slugIdentity(abs)
	}

	home, _ := os.UserHomeDir()
	if home != "" {
		if resolved, err := filepath.EvalSymlinks(home); err == nil {
			home = resolved
		}
	}

	if root == home || root == "/" {
		return slugIdentity(abs)
	}

	remote := canonicalRemote(gitValue(abs, "remote", "get-url", "origin"))

	name := filepath.Base(mainRoot(common))
	material := common

	if remote != "" {
		name = filepath.Base(remote)
		material = remote
	}

	return Identity{
		ID:     slugify(name) + "-" + shortHash(material),
		Root:   root,
		Remote: remote,
	}
}

func mainRoot(common string) string {
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common)
	}

	return common
}

func slugIdentity(abs string) Identity {
	return Identity{ID: memory.Slug(abs), Slugs: true}
}

func gitValue(dir string, args ...string) string {
	lines, err := gitLines(dir, args...)
	if err != nil || len(lines) == 0 {
		return ""
	}

	return strings.TrimSpace(lines[0])
}

func gitLines(dir string, args ...string) ([]string, error) {
	all := append([]string{"-C", dir}, args...)

	out, err := exec.CommandContext(context.Background(), "git", all...).Output() //nolint:gosec // G204: fixed git subcommands only
	if err != nil {
		return nil, err
	}

	return strings.Split(strings.TrimRight(string(out), "\n"), "\n"), nil
}

func canonicalRemote(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}

	if _, after, ok := strings.Cut(value, "://"); ok {
		value = after
	}

	value = stripUser(value)
	value = scpToPath(value)
	value = strings.TrimSuffix(value, "/")
	value = strings.TrimSuffix(value, ".git")

	if value == "" {
		return ""
	}

	return strings.ToLower(value)
}

func stripUser(value string) string {
	at := strings.IndexByte(value, '@')
	if at < 0 {
		return value
	}

	slash := strings.IndexByte(value, '/')
	if slash >= 0 && slash < at {
		return value
	}

	return value[at+1:]
}

func scpToPath(value string) string {
	colon := strings.IndexByte(value, ':')
	if colon < 0 {
		return value
	}

	slash := strings.IndexByte(value, '/')
	if slash >= 0 && slash < colon {
		return value
	}

	return value[:colon] + "/" + value[colon+1:]
}

func slugify(value string) string {
	slug := slugPattern.ReplaceAllString(strings.ToLower(value), "-")
	slug = strings.Trim(slug, "-")

	if len(slug) > slugMaxLen {
		slug = strings.Trim(slug[:slugMaxLen], "-")
	}

	if slug == "" {
		return "repo"
	}

	return slug
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))

	return hex.EncodeToString(sum[:])[:hashLen]
}
