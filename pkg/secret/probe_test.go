package secret_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/secret"
)

// TestNeedsProbe pins the gate the doctor uses before touching the system
// keychain: an empty keyring store has nothing to verify, and probing it opens
// the access dialog (with its destructive "Reset To Defaults" button) for
// nothing.
func TestNeedsProbe(t *testing.T) {
	writeDoc := func(t *testing.T, body string) string {
		t.Helper()

		path := filepath.Join(t.TempDir(), "secrets.json")
		So(os.WriteFile(path, []byte(body), 0o600), ShouldBeNil)

		return path
	}

	Convey("Given a file-backed store", t, func() {
		store, err := secret.Load(writeDoc(t, `{"version":1,"secrets":{"TOKEN":"value"}}`))
		So(err, ShouldBeNil)

		Convey("Then no probe is needed", func() {
			So(store.NeedsProbe(), ShouldBeFalse)
		})
	})

	Convey("Given a keyring store with no recorded value", t, func() {
		store, err := secret.Load(writeDoc(t, `{"version":2,"backend":"keyring","secrets":{}}`),
			secret.WithKeyring(newFakeKeyring(map[string]string{})))
		So(err, ShouldBeNil)

		Convey("Then nothing is probed and the keychain is never read", func() {
			So(store.Backend(), ShouldEqual, secret.BackendKeyring)
			So(store.NeedsProbe(), ShouldBeFalse)
		})
	})

	Convey("Given a keyring store that records a reference the keychain does not answer", t, func() {
		store, err := secret.Load(writeDoc(t, `{"version":2,"backend":"keyring","secrets":{"TOKEN":""}}`),
			secret.WithKeyring(newFakeKeyring(map[string]string{})))
		So(err, ShouldBeNil)

		Convey("Then the probe still runs: the reference is what needs verifying", func() {
			So(store.NeedsProbe(), ShouldBeTrue)
		})
	})

	Convey("Given a keyring store that records a reference with a value", t, func() {
		store, err := secret.Load(writeDoc(t, `{"version":2,"backend":"keyring","secrets":{"TOKEN":""}}`),
			secret.WithKeyring(newFakeKeyring(map[string]string{"TOKEN": "value"})))
		So(err, ShouldBeNil)

		Convey("Then the probe is needed and passes", func() {
			So(store.NeedsProbe(), ShouldBeTrue)
			So(store.Probe(), ShouldBeNil)
		})
	})
}
