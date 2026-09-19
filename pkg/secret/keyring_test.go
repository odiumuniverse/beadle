package secret

import (
	"encoding/hex"
	"errors"
	"os/exec"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

type runnerFunc func(name string, args []string, stdin []byte) ([]byte, int, error)

func (f runnerFunc) Run(name string, args []string, stdin []byte) ([]byte, int, error) {
	return f(name, args, stdin)
}

func testShellKeyring(t *testing.T, tool string, runner Runner) *shellKeyring {
	t.Helper()

	keyring := &shellKeyring{
		runner:   runner,
		tool:     tool,
		notFound: darwinMissing,
		lookPath: func(string) (string, error) { return "/usr/bin/" + tool, nil },
	}

	if tool == linuxTool {
		keyring.notFound = linuxMissing
	}

	return keyring
}

type keyringCall struct {
	args  []string
	stdin []byte
}

func recordingRunner(t *testing.T, calls *[]keyringCall, reply func(call keyringCall) ([]byte, int, error)) Runner {
	t.Helper()

	return runnerFunc(func(name string, args []string, stdin []byte) ([]byte, int, error) {
		call := keyringCall{args: args, stdin: stdin}

		*calls = append(*calls, call)

		if name != darwinTool && name != linuxTool {
			t.Errorf("unexpected tool %q", name)
		}

		return reply(call)
	})
}

func exitError() error {
	return &exec.ExitError{}
}

func TestKeyringGetFoundDarwin(t *testing.T) {
	t.Parallel()

	var calls []keyringCall

	keyring := testShellKeyring(t, darwinTool, recordingRunner(t, &calls, func(keyringCall) ([]byte, int, error) {
		return []byte(encodePayload("s3cr3t") + "\n"), 0, nil
	}))

	value, found, err := keyring.Get("ALPHA")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "s3cr3t", value)

	require.Equal(t, []string{"find-generic-password", "-s", keyringService, "-a", "ALPHA", "-w"}, calls[0].args)
	require.Nil(t, calls[0].stdin)
}

func TestKeyringGetFoundLinux(t *testing.T) {
	t.Parallel()

	var calls []keyringCall

	keyring := testShellKeyring(t, linuxTool, recordingRunner(t, &calls, func(keyringCall) ([]byte, int, error) {
		return []byte(encodePayload("pem\nline") + "\n"), 0, nil
	}))

	value, found, err := keyring.Get("ALPHA")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "pem\nline", value, "only the tool's trailing newline is trimmed")

	require.Equal(t, []string{"lookup", "service", keyringService, "account", "ALPHA"}, calls[0].args)
}

func TestKeyringGetNotFound(t *testing.T) {
	t.Parallel()

	for tool, code := range map[string]int{darwinTool: darwinMissing, linuxTool: linuxMissing} {
		var calls []keyringCall

		keyring := testShellKeyring(t, tool, recordingRunner(t, &calls, func(keyringCall) ([]byte, int, error) {
			return nil, code, exitError()
		}))

		value, found, err := keyring.Get("ALPHA")
		require.NoError(t, err)
		require.False(t, found)
		require.Empty(t, value)
	}
}

func TestKeyringGetFailure(t *testing.T) {
	t.Parallel()

	var calls []keyringCall

	keyring := testShellKeyring(t, darwinTool, recordingRunner(t, &calls, func(keyringCall) ([]byte, int, error) {
		return nil, 36, errors.New("security: SecKeychainSearchCopyNext: The specified item could not be found")
	}))

	_, _, err := keyring.Get("ALPHA")
	require.ErrorContains(t, err, "keyring get ALPHA")
}

func TestKeyringSetUsesHexTransport(t *testing.T) {
	t.Parallel()

	var calls []keyringCall

	keyring := testShellKeyring(t, darwinTool, recordingRunner(t, &calls, func(keyringCall) ([]byte, int, error) {
		return nil, 0, nil
	}))

	value := "line1\nline2 $ecret"

	require.NoError(t, keyring.Set("ALPHA", value))
	require.Len(t, calls, 1)

	payload := calls[0].args[len(calls[0].args)-1]
	require.Equal(t, []string{"add-generic-password", "-U", "-s", keyringService, "-a", "ALPHA", "-X", payload}, calls[0].args)
	require.Nil(t, calls[0].stdin)

	decoded, err := hex.DecodeString(payload)
	require.NoError(t, err)
	require.Equal(t, encodePayload(value), string(decoded))
	require.Equal(t, "v1:"+hex.EncodeToString([]byte(value)), string(decoded), "the stored payload is always printable ASCII")

	for _, arg := range calls[0].args {
		require.NotContains(t, arg, value, "the plaintext must never reach argv")
		require.NotContains(t, arg, "line1", "the plaintext must never reach argv")
	}
}

func TestKeyringSetUsesStdinOnLinux(t *testing.T) {
	t.Parallel()

	var calls []keyringCall

	keyring := testShellKeyring(t, linuxTool, recordingRunner(t, &calls, func(keyringCall) ([]byte, int, error) {
		return nil, 0, nil
	}))

	value := "line1\nline2 $ecret"

	require.NoError(t, keyring.Set("ALPHA", value))
	require.Len(t, calls, 1)

	require.Equal(t, []string{"store", "--label=" + keyringService + ": ALPHA", "service", keyringService, "account", "ALPHA"}, calls[0].args)
	require.Equal(t, encodePayload(value), string(calls[0].stdin))
	require.Contains(t, string(calls[0].stdin), payloadPrefix)
	require.NotContains(t, string(calls[0].stdin), value, "the plaintext never reaches stdin")
}

func TestKeyringLinuxRoundTripKeepsTrailingNewline(t *testing.T) {
	t.Parallel()

	value := "line1\nline2\n"

	var stored []byte

	keyring := testShellKeyring(t, linuxTool, runnerFunc(func(_ string, args []string, stdin []byte) ([]byte, int, error) {
		if args[0] == "store" {
			stored = append([]byte(nil), stdin...)

			return nil, 0, nil
		}

		return append(append([]byte(nil), stored...), '\n'), 0, nil
	}))

	require.NoError(t, keyring.Set("ALPHA", value))
	require.Equal(t, encodePayload(value), string(stored))

	got, found, err := keyring.Get("ALPHA")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, value, got, "a value ending with a newline survives the round-trip")
}

func TestKeyringSetFailure(t *testing.T) {
	t.Parallel()

	var calls []keyringCall

	keyring := testShellKeyring(t, darwinTool, recordingRunner(t, &calls, func(keyringCall) ([]byte, int, error) {
		return nil, 1, errors.New("security: write denied")
	}))

	err := keyring.Set("ALPHA", "s3cr3t")
	require.ErrorContains(t, err, "keyring set ALPHA")
	require.NotContains(t, err.Error(), "s3cr3t")
}

func TestKeyringDeleteRemovesEntry(t *testing.T) {
	t.Parallel()

	var calls []keyringCall

	keyring := testShellKeyring(t, darwinTool, recordingRunner(t, &calls, func(call keyringCall) ([]byte, int, error) {
		if call.args[0] == "delete-generic-password" {
			return nil, 0, nil
		}

		return nil, darwinMissing, exitError()
	}))

	removed, err := keyring.Delete("ALPHA")
	require.NoError(t, err)
	require.True(t, removed)

	require.Len(t, calls, 2, "delete is followed by a read-back")
	require.Equal(t, []string{"delete-generic-password", "-s", keyringService, "-a", "ALPHA"}, calls[0].args)
	require.Equal(t, "find-generic-password", calls[1].args[0])
}

func TestKeyringDeleteNotFound(t *testing.T) {
	t.Parallel()

	var calls []keyringCall

	keyring := testShellKeyring(t, darwinTool, recordingRunner(t, &calls, func(keyringCall) ([]byte, int, error) {
		return nil, darwinMissing, exitError()
	}))

	removed, err := keyring.Delete("ALPHA")
	require.NoError(t, err)
	require.False(t, removed)
}

func TestKeyringDeleteReadBackProof(t *testing.T) {
	t.Parallel()

	var calls []keyringCall

	keyring := testShellKeyring(t, darwinTool, recordingRunner(t, &calls, func(call keyringCall) ([]byte, int, error) {
		if call.args[0] == "delete-generic-password" {
			return nil, 0, nil
		}

		return []byte("still here\n"), 0, nil
	}))

	removed, err := keyring.Delete("ALPHA")
	require.ErrorContains(t, err, "still present")
	require.False(t, removed)
}

func TestKeyringGetReturnsForeignRawValue(t *testing.T) {
	t.Parallel()

	var calls []keyringCall

	keyring := testShellKeyring(t, darwinTool, recordingRunner(t, &calls, func(keyringCall) ([]byte, int, error) {
		return []byte("hand-added password\n"), 0, nil
	}))

	value, found, err := keyring.Get("ALPHA")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "hand-added password", value, "items added outside agent-sync are returned as-is")
}

func TestKeyringGetRejectsCorruptPayload(t *testing.T) {
	t.Parallel()

	var calls []keyringCall

	keyring := testShellKeyring(t, darwinTool, recordingRunner(t, &calls, func(keyringCall) ([]byte, int, error) {
		return []byte(payloadPrefix + "zz\n"), 0, nil
	}))

	_, _, err := keyring.Get("ALPHA")
	require.ErrorIs(t, err, errCorruptPayload)
}

func TestKeyringLookPathIsLazy(t *testing.T) {
	t.Parallel()

	called := 0

	keyring := &shellKeyring{
		runner: runnerFunc(func(string, []string, []byte) ([]byte, int, error) {
			called++

			return nil, 0, nil
		}),
		tool:     darwinTool,
		notFound: darwinMissing,
		lookPath: func(string) (string, error) { return "", exec.ErrNotFound },
	}

	_, _, err := keyring.Get("ALPHA")
	require.ErrorIs(t, err, ErrKeyringUnavailable)
	require.ErrorContains(t, err, darwinTool)

	require.ErrorIs(t, keyring.Set("ALPHA", "s3cr3t"), ErrKeyringUnavailable)

	_, err = keyring.Delete("ALPHA")
	require.ErrorIs(t, err, ErrKeyringUnavailable)

	require.Zero(t, called, "the tool is never executed when it is missing")
}

func TestKeyringSupportedPlatforms(t *testing.T) {
	t.Parallel()

	switch runtime.GOOS {
	case "darwin", "linux":
		keyring, err := NewShellKeyring(ExecRunner{})
		require.NoError(t, err)
		require.NotNil(t, keyring)
	default:
		_, err := NewShellKeyring(ExecRunner{})
		require.ErrorIs(t, err, ErrKeyringUnsupported)
	}
}
