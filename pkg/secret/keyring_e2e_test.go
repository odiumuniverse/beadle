package secret_test

import (
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/secret"
)

func TestKeyringRealRoundTrip(t *testing.T) {
	if os.Getenv("BEADLE_KEYRING_E2E") != "1" {
		t.Skip("set BEADLE_KEYRING_E2E=1 to touch the real keychain")
	}

	if runtime.GOOS != "darwin" {
		t.Skip("the round-trip documents the macOS transport; the Linux mapping is untested")
	}

	Convey("Given the real shell keyring", t, func() {
		keyring, err := secret.NewShellKeyring(secret.ExecRunner{})
		So(err, ShouldBeNil)

		account := fmt.Sprintf("beadle-e2e-%d", time.Now().UnixNano())

		t.Cleanup(func() {
			_, _ = keyring.Delete(account)
		})

		value := "s3cr3t\nPEM line \u2603"

		Convey("When a value with newlines and unicode is stored and read back", func() {
			So(keyring.Set(account, value), ShouldBeNil)

			got, found, err := keyring.Get(account)

			Convey("Then it round-trips and can be deleted", func() {
				So(err, ShouldBeNil)
				So(found, ShouldBeTrue)
				So(got, ShouldEqual, value)

				removed, err := keyring.Delete(account)
				So(err, ShouldBeNil)
				So(removed, ShouldBeTrue)

				_, found, err = keyring.Get(account)
				So(err, ShouldBeNil)
				So(found, ShouldBeFalse)
			})
		})
	})
}
