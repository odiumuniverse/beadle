package secret

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

var (
	// ErrKeyringUnavailable reports that the platform keyring tool is missing.
	ErrKeyringUnavailable = errors.New("keyring is unavailable")
	// ErrKeyringUnsupported reports that the platform has no keyring mapping.
	ErrKeyringUnsupported = errors.New("keyring is not supported on this platform")
)

const (
	keyringService = "agent-sync"
	darwinTool     = "security"
	linuxTool      = "secret-tool"
	darwinMissing  = 44
	linuxMissing   = 1
	payloadPrefix  = "v1:"
)

var errCorruptPayload = errors.New("unexpected keyring payload")

func encodePayload(value string) string {
	return payloadPrefix + hex.EncodeToString([]byte(value))
}

func decodePayload(raw string) (string, error) {
	encoded, ok := strings.CutPrefix(raw, payloadPrefix)
	if !ok {
		return raw, nil
	}

	value, err := hex.DecodeString(encoded)
	if err != nil {
		return "", errCorruptPayload
	}

	return string(value), nil
}

type Runner interface {
	Run(name string, args []string, stdin []byte) (stdout []byte, code int, err error)
}

type ExecRunner struct{}

func (ExecRunner) Run(name string, args []string, stdin []byte) ([]byte, int, error) {
	var stdout, stderr bytes.Buffer

	cmd := exec.CommandContext(context.Background(), name, args...) //nolint:gosec // G204: only the platform keyring tool is executed
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			return stdout.Bytes(), exitErr.ExitCode(), fmt.Errorf("%s: %w: %s", name, exitErr, strings.TrimSpace(stderr.String()))
		}

		return stdout.Bytes(), -1, fmt.Errorf("%s: %w", name, err)
	}

	return stdout.Bytes(), 0, nil
}

type Keyring interface {
	Get(account string) (string, bool, error)
	Set(account, value string) error
	Delete(account string) (bool, error)
}

type shellKeyring struct {
	runner   Runner
	tool     string
	notFound int
	lookPath func(string) (string, error)
}

func NewShellKeyring(r Runner) (Keyring, error) {
	keyring := &shellKeyring{runner: r, lookPath: exec.LookPath}

	switch runtime.GOOS {
	case "darwin":
		keyring.tool, keyring.notFound = darwinTool, darwinMissing
	case "linux":
		keyring.tool, keyring.notFound = linuxTool, linuxMissing
	default:
		return nil, fmt.Errorf("%w: %s", ErrKeyringUnsupported, runtime.GOOS)
	}

	return keyring, nil
}

func (k *shellKeyring) available() error {
	if _, err := k.lookPath(k.tool); err != nil {
		return fmt.Errorf("%w: %s not found in PATH", ErrKeyringUnavailable, k.tool)
	}

	return nil
}

func (k *shellKeyring) Get(account string) (string, bool, error) {
	if err := k.available(); err != nil {
		return "", false, err
	}

	stdout, code, err := k.runner.Run(k.tool, k.getArgs(account), nil)

	switch {
	case code == k.notFound:
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("keyring get %s: %w", account, err)
	case code != 0:
		return "", false, fmt.Errorf("keyring get %s: exit code %d", account, code)
	}

	return k.decodeValue(account, stdout)
}

func (k *shellKeyring) decodeValue(account string, stdout []byte) (string, bool, error) {
	raw := strings.TrimSuffix(string(stdout), "\n")

	value, err := decodePayload(raw)
	if err != nil {
		return "", false, fmt.Errorf("keyring get %s: %w", account, err)
	}

	return value, true, nil
}

func (k *shellKeyring) Set(account, value string) error {
	if err := k.available(); err != nil {
		return err
	}

	args, stdin := k.setCommand(account, value)

	_, code, err := k.runner.Run(k.tool, args, stdin)
	if err == nil && code == 0 {
		return nil
	}

	if err == nil {
		err = fmt.Errorf("exit code %d", code)
	}

	return fmt.Errorf("keyring set %s: %w", account, err)
}

func (k *shellKeyring) Delete(account string) (bool, error) {
	if err := k.available(); err != nil {
		return false, err
	}

	_, code, err := k.runner.Run(k.tool, k.deleteArgs(account), nil)
	if err != nil && code != k.notFound {
		return false, fmt.Errorf("keyring delete %s: %w", account, err)
	}

	if code != 0 && code != k.notFound {
		return false, fmt.Errorf("keyring delete %s: exit code %d", account, code)
	}

	_, found, err := k.Get(account)
	if err != nil {
		return false, fmt.Errorf("keyring delete %s: read-back: %w", account, err)
	}

	if found {
		return false, fmt.Errorf("keyring delete %s: the entry is still present", account)
	}

	return code == 0, nil
}

func (k *shellKeyring) getArgs(account string) []string {
	if k.tool == linuxTool {
		return []string{"lookup", "service", keyringService, "account", account}
	}

	return []string{"find-generic-password", "-s", keyringService, "-a", account, "-w"}
}

func (k *shellKeyring) setCommand(account, value string) ([]string, []byte) {
	if k.tool == linuxTool {
		args := []string{"store", "--label=" + keyringService + ": " + account, "service", keyringService, "account", account}

		return args, []byte(encodePayload(value))
	}

	args := []string{"add-generic-password", "-U", "-s", keyringService, "-a", account, "-X", hex.EncodeToString([]byte(encodePayload(value)))}

	return args, nil
}

func (k *shellKeyring) deleteArgs(account string) []string {
	if k.tool == linuxTool {
		return []string{"clear", "service", keyringService, "account", account}
	}

	return []string{"delete-generic-password", "-s", keyringService, "-a", account}
}
