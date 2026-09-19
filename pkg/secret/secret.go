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

	"github.com/odiumuniverse/beadle/pkg/fsutil"
)

const FileName = "secrets.json"

const (
	ModeLiteral = "literal"
	ModeEnv     = "env"
)

const (
	BackendFile    = "file"
	BackendKeyring = "keyring"
)

const (
	refPrefix = "{secret:"
	envPrefix = "{env:"
	refSuffix = "}"

	fileVersion    = 1
	keyringVersion = 2
	fingerprintN   = 8
	probeAccount   = "agentsync-doctor-probe"
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
	path       string
	values     map[string]string
	backend    string
	keyring    Keyring
	keyringErr error
	touched    map[string]struct{}
	deleted    map[string]struct{}
	migrated   bool
	changed    bool
}

type document struct {
	Version int               `json:"version"`
	Backend string            `json:"backend,omitempty"`
	Secrets map[string]string `json:"secrets"`
}

type Option func(*Store)

func WithKeyring(keyring Keyring) Option {
	return func(s *Store) { s.keyring = keyring }
}

func Load(path string, opts ...Option) (*Store, error) {
	store := &Store{path: path, values: map[string]string{}, backend: BackendFile}

	for _, opt := range opts {
		opt(store)
	}

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

	switch doc.Backend {
	case "", BackendFile:
	case BackendKeyring:
		store.backend = BackendKeyring
	default:
		return nil, fmt.Errorf("parse secrets: unknown backend %q", doc.Backend)
	}

	if doc.Secrets != nil {
		store.values = doc.Secrets
	}

	store.prefetch()

	return store, nil
}

func (s *Store) prefetch() {
	if s.backend != BackendKeyring {
		return
	}

	keyring, err := s.shellKeyring()
	if err != nil {
		s.keyringErr = err
		s.values = map[string]string{}

		return
	}

	values := make(map[string]string, len(s.values))

	for _, name := range slices.Sorted(maps.Keys(s.values)) {
		value, found, err := keyring.Get(name)
		if err != nil {
			s.keyringErr = err
			s.values = map[string]string{}

			return
		}

		if found {
			values[name] = value
		}
	}

	s.values = values
}

func (s *Store) shellKeyring() (Keyring, error) {
	if s.keyring != nil {
		return s.keyring, nil
	}

	keyring, err := NewShellKeyring(ExecRunner{})
	if err != nil {
		return nil, err
	}

	s.keyring = keyring

	return keyring, nil
}

func (s *Store) Backend() string {
	return s.backend
}

func (s *Store) KeyringErr() error {
	return s.keyringErr
}

func (s *Store) Probe() error {
	if s.keyringErr != nil {
		return s.keyringErr
	}

	if s.backend != BackendKeyring {
		return nil
	}

	keyring, err := s.shellKeyring()
	if err != nil {
		return err
	}

	if _, _, err := keyring.Get(probeAccount); err != nil {
		return fmt.Errorf("keyring probe: %w", err)
	}

	return nil
}

func (s *Store) SetBackend(backend string) error {
	switch backend {
	case BackendFile, BackendKeyring:
	default:
		return fmt.Errorf("unknown secrets backend %q", backend)
	}

	if backend == s.backend {
		return nil
	}

	s.backend = backend
	s.migrated = backend == BackendKeyring
	s.changed = true

	return nil
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

	if s.touched == nil {
		s.touched = map[string]struct{}{}
	}

	s.touched[name] = struct{}{}

	delete(s.deleted, name)
}

func (s *Store) Delete(name string) bool {
	if _, ok := s.values[name]; !ok {
		return false
	}

	delete(s.values, name)

	s.changed = true

	if s.deleted == nil {
		s.deleted = map[string]struct{}{}
	}

	s.deleted[name] = struct{}{}

	delete(s.touched, name)

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

	if err := s.saveKeyring(); err != nil {
		return err
	}

	data, err := json.MarshalIndent(s.document(), "", "  ")
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
	s.migrated = false
	s.touched = nil
	s.deleted = nil

	return nil
}

func (s *Store) document() document {
	if s.backend != BackendKeyring {
		return document{Version: fileVersion, Secrets: s.values}
	}

	secrets := make(map[string]string, len(s.values))

	for name := range s.values {
		secrets[name] = ""
	}

	return document{Version: keyringVersion, Backend: BackendKeyring, Secrets: secrets}
}

func (s *Store) saveKeyring() error {
	if s.backend != BackendKeyring {
		return nil
	}

	if s.keyringErr != nil {
		return fmt.Errorf("save secrets to the keyring: %w", s.keyringErr)
	}

	keyring, err := s.shellKeyring()
	if err != nil {
		return fmt.Errorf("save secrets to the keyring: %w", err)
	}

	for _, name := range s.upsertNames() {
		if err := keyring.Set(name, s.values[name]); err != nil {
			return fmt.Errorf("save secrets to the keyring: %w", err)
		}
	}

	for _, name := range slices.Sorted(maps.Keys(s.deleted)) {
		if _, err := keyring.Delete(name); err != nil {
			return fmt.Errorf("remove secrets from the keyring: %w", err)
		}
	}

	return nil
}

func (s *Store) upsertNames() []string {
	if s.migrated {
		return slices.Sorted(maps.Keys(s.values))
	}

	return slices.Sorted(maps.Keys(s.touched))
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
