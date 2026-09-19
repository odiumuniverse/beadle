package secret_test

import (
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/odiumuniverse/beadle/pkg/secret"
)

func TestKeyringRealRoundTrip(t *testing.T) { //nolint:paralleltest // touches the real keychain
	if os.Getenv("AGENTSYNC_KEYRING_E2E") != "1" {
		t.Skip("set AGENTSYNC_KEYRING_E2E=1 to touch the real keychain")
	}

	if runtime.GOOS != "darwin" {
		t.Skip("the round-trip documents the macOS transport; the Linux mapping is untested")
	}

	keyring, err := secret.NewShellKeyring(secret.ExecRunner{})
	require.NoError(t, err)

	account := fmt.Sprintf("agentsync-e2e-%d", time.Now().UnixNano())

	t.Cleanup(func() {
		_, _ = keyring.Delete(account)
	})

	value := "s3cr3t\nPEM line \u2603"

	require.NoError(t, keyring.Set(account, value))

	got, found, err := keyring.Get(account)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, value, got, "the -X hex transport preserves newlines and unicode")

	removed, err := keyring.Delete(account)
	require.NoError(t, err)
	require.True(t, removed)

	_, found, err = keyring.Get(account)
	require.NoError(t, err)
	require.False(t, found)
}
