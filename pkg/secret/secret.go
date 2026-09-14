package secret

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
	"regexp"
	"slices"
	"strings"

	"github.com/odiumuniverse/agents-sync/pkg/fsutil"
)

const FileName = "secrets.json"

const (
	ModeLiteral = "literal"
	ModeEnv     = "env"
)

const (
	refPrefix = "{secret:"
	envPrefix = "{env:"
	refSuffix = "}"

	fileVersion  = 1
	fingerprintN = 8
)

var namePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var embeddedEnvRef = regexp.MustCompile(`\{env:[A-Za-z_][A-Za-z0-9_]*\}`)

var keyHints = []string{
	"api_key",
	"apikey",
	"access_key",
	"secret",
	"token",
	"password",
	"passwd",
	"credential",
	"authorization",
	"auth",
	"cookie",
	"session",
	"private_key",
	"bearer",
	"signature",
}

var valueHints = []string{
	"bearer ",
	"basic ",
	"token ",
	"sk-",
	"ghp_",
	"gho_",
	"github_pat_",
	"glpat-",
	"xoxb-",
	"xoxp-",
	"akia",
	"ya29.",
	"-----begin ",
}

type Store struct {
	path    string
	values  map[string]string
	changed bool
}

type document struct {
	Version int               `json:"version"`
	Secrets map[string]string `json:"secrets"`
}

func Load(path string) (*Store, error) {
	store := &Store{path: path, values: map[string]string{}}

	data, err := os.ReadFile(path) //nolint:gosec // G304: path is the vault secrets file
	if errors.Is(err, fs.ErrNotExist) {
		return store, nil
	}

	if err != nil {
		return nil, fmt.Errorf("read secrets: %w", err)
	}

	if len(data) == 0 {
		return store, nil
	}

	doc := document{}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse secrets: %w", err)
	}

	if doc.Secrets != nil {
		store.values = doc.Secrets
	}

	return store, nil
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) Changed() bool {
	return s.changed
}

func (s *Store) Get(name string) (string, bool) {
	value, ok := s.values[name]

	return value, ok
}

func (s *Store) Has(name string) bool {
	_, ok := s.values[name]

	return ok
}

func (s *Store) Set(name, value string) {
	if current, ok := s.values[name]; ok && current == value {
		return
	}

	s.values[name] = value
	s.changed = true
}

func (s *Store) Delete(name string) bool {
	if _, ok := s.values[name]; !ok {
		return false
	}

	delete(s.values, name)
	s.changed = true

	return true
}

func (s *Store) Names() []string {
	return slices.Sorted(maps.Keys(s.values))
}

func (s *Store) Len() int {
	return len(s.values)
}

func (s *Store) Save() error {
	if !s.changed {
		return nil
	}

	data, err := json.MarshalIndent(document{Version: fileVersion, Secrets: s.values}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode secrets: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create secrets directory: %w", err)
	}

	if err := fsutil.WriteFileAtomic(s.path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write secrets: %w", err)
	}

	s.changed = false

	return nil
}

func (s *Store) NameFor(key, value string) string {
	base := NormalizeName(key)

	if existing, ok := s.nameOfValue(base, value); ok {
		return existing
	}

	if current, ok := s.values[base]; ok && current != value {
		return base + "_" + Fingerprint(value)
	}

	return base
}

func (s *Store) nameOfValue(base, value string) (string, bool) {
	for _, name := range s.Names() {
		if name != base && !strings.HasPrefix(name, base+"_") {
			continue
		}

		if s.values[name] == value {
			return name, true
		}
	}

	return "", false
}

func NormalizeName(key string) string {
	name := strings.ToUpper(strings.TrimSpace(key))
	name = strings.Map(func(r rune) rune {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}

		return '_'
	}, name)

	for strings.Contains(name, "__") {
		name = strings.ReplaceAll(name, "__", "_")
	}

	name = strings.Trim(name, "_")

	if name == "" {
		return "SECRET"
	}

	if name[0] >= '0' && name[0] <= '9' {
		return "S_" + name
	}

	return name
}

func Fingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))

	return strings.ToUpper(hex.EncodeToString(sum[:]))[:fingerprintN]
}

func ValidName(name string) bool {
	return namePattern.MatchString(name)
}

func Ref(name string) string {
	return refPrefix + name + refSuffix
}

func EnvRef(name string) string {
	return envPrefix + name + refSuffix
}

func ParseRef(value string) (string, bool) {
	name, ok := strings.CutPrefix(value, refPrefix)
	if !ok {
		return "", false
	}

	return strings.CutSuffix(name, refSuffix)
}

func IsRef(value string) bool {
	_, ok := ParseRef(value)

	return ok
}

func ParseEnvRef(value string) (string, bool) {
	name, ok := strings.CutPrefix(value, envPrefix)
	if !ok {
		return "", false
	}

	return strings.CutSuffix(name, refSuffix)
}

func IsSecret(key, value string) bool {
	if value == "" || IsRef(value) {
		return false
	}

	if _, ok := ParseEnvRef(value); ok {
		return false
	}

	if embeddedEnvRef.MatchString(value) {
		return false
	}

	lower := strings.ToLower(key)

	if lower == "key" || strings.HasSuffix(lower, "_key") {
		return true
	}

	if slices.ContainsFunc(keyHints, func(hint string) bool { return strings.Contains(lower, hint) }) {
		return true
	}

	trimmed := strings.ToLower(strings.TrimSpace(value))

	return slices.ContainsFunc(valueHints, func(hint string) bool { return strings.HasPrefix(trimmed, hint) })
}
