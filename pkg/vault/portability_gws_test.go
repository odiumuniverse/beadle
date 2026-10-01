package vault_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/odiumuniverse/beadle/pkg/fsutil"
	"github.com/odiumuniverse/beadle/pkg/vault"
)

// A vault that travels in git carries what another machine needs and nothing
// that is true of one machine: the spec and the lock travel, and everything
// under the library's home that records what *this* machine did (receipts,
// journal, tombstones, consent, secrets, trust, the watch lease, the store)
// stays behind.
//
// The assertion is about coverage, not about literal lines: git ignores a path
// when a rule names its directory, so naming `verger/state/` covers every file
// under it and that is the rule a fresh vault should carry.
func TestTheVaultIgnoresMachineLocalState(t *testing.T) {
	Convey("Given a fresh vault", t, func() {
		root := filepath.Join(t.TempDir(), "vault")
		v := vault.New(root)

		So(v.Init(), ShouldBeNil)

		ignore, err := os.ReadFile(filepath.Join(root, ".gitignore")) //nolint:gosec // G304: the test reads the .gitignore of the temp vault it just created
		So(err, ShouldBeNil)

		rules := []string{}

		for line := range strings.SplitSeq(string(ignore), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				rules = append(rules, line)
			}
		}

		// ignored reports whether some rule covers the path, the way git does.
		ignored := func(path string) bool {
			for _, rule := range rules {
				if fsutil.Under(strings.TrimSuffix(rule, "/"), path) {
					return true
				}
			}

			return false
		}

		machineLocal := []string{
			"verger/state/receipts/",
			"verger/state/journal.jsonl",
			"verger/state/tombstones.json",
			"verger/state/consent.json",
			"verger/state/secrets.json",
			"verger/state/trust.json",
			"verger/state/watch.lease",
			"verger/state/.lock",
			"verger/store/",
		}

		Convey("Then the portable files travel", func() {
			So(ignored("verger/verger.toml"), ShouldBeFalse)
			So(ignored("verger/verger.lock"), ShouldBeFalse)
		})

		Convey("Then everything the library records per machine stays behind", func() {
			for _, path := range machineLocal {
				So(ignored(path), ShouldBeTrue)
			}
		})
	})
}
