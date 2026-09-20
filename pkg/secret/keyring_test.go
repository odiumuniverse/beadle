package secret

import (
	"encoding/hex"
	"errors"
	"os/exec"
	"runtime"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
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
	Convey("Given a macOS keyring returning an encoded payload", t, func() {
		var calls []keyringCall

		keyring := testShellKeyring(t, darwinTool, recordingRunner(t, &calls, func(keyringCall) ([]byte, int, error) {
			return []byte(encodePayload("s3cr3t") + "\n"), 0, nil
		}))

		Convey("When the value is fetched", func() {
			value, found, err := keyring.Get("ALPHA")

			Convey("Then it decodes and uses find-generic-password with a password on argv", func() {
				So(err, ShouldBeNil)
				So(found, ShouldBeTrue)
				So(value, ShouldEqual, "s3cr3t")
				So(calls[0].args, ShouldResemble, []string{"find-generic-password", "-s", keyringService, "-a", "ALPHA", "-w"})
				So(calls[0].stdin, ShouldBeNil)
			})
		})
	})
}

func TestKeyringGetFoundLinux(t *testing.T) {
	Convey("Given a linux keyring returning a payload with a newline", t, func() {
		var calls []keyringCall

		keyring := testShellKeyring(t, linuxTool, recordingRunner(t, &calls, func(keyringCall) ([]byte, int, error) {
			return []byte(encodePayload("pem\nline") + "\n"), 0, nil
		}))

		Convey("When the value is fetched", func() {
			value, found, err := keyring.Get("ALPHA")

			Convey("Then only the tool's trailing newline is trimmed", func() {
				So(err, ShouldBeNil)
				So(found, ShouldBeTrue)
				So(value, ShouldEqual, "pem\nline")
				So(calls[0].args, ShouldResemble, []string{"lookup", "service", keyringService, "account", "ALPHA"})
			})
		})
	})
}

func TestKeyringGetNotFound(t *testing.T) {
	Convey("Given a keyring that reports a missing item", t, func() {
		for tool, code := range map[string]int{darwinTool: darwinMissing, linuxTool: linuxMissing} {
			var calls []keyringCall

			keyring := testShellKeyring(t, tool, recordingRunner(t, &calls, func(keyringCall) ([]byte, int, error) {
				return nil, code, exitError()
			}))

			Convey("When the value is fetched from "+tool, func() {
				value, found, err := keyring.Get("ALPHA")

				Convey("Then it reports absence without error", func() {
					So(err, ShouldBeNil)
					So(found, ShouldBeFalse)
					So(value, ShouldBeEmpty)
				})
			})
		}
	})
}

func TestKeyringGetFailure(t *testing.T) {
	Convey("Given a keyring that fails with an unrelated error", t, func() {
		var calls []keyringCall

		keyring := testShellKeyring(t, darwinTool, recordingRunner(t, &calls, func(keyringCall) ([]byte, int, error) {
			return nil, 36, errors.New("security: SecKeychainSearchCopyNext: The specified item could not be found")
		}))

		Convey("When the value is fetched", func() {
			_, _, err := keyring.Get("ALPHA")

			Convey("Then the error is reported", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "keyring get ALPHA")
			})
		})
	})
}

func TestKeyringSetUsesHexTransport(t *testing.T) {
	Convey("Given a macOS keyring", t, func() {
		var calls []keyringCall

		keyring := testShellKeyring(t, darwinTool, recordingRunner(t, &calls, func(keyringCall) ([]byte, int, error) {
			return nil, 0, nil
		}))

		value := "line1\nline2 $ecret"

		Convey("When a value is stored", func() {
			So(keyring.Set("ALPHA", value), ShouldBeNil)
			So(calls, ShouldHaveLength, 1)

			payload := calls[0].args[len(calls[0].args)-1]

			decoded, err := hex.DecodeString(payload)

			Convey("Then it is hex-encoded and never reaches argv in plaintext", func() {
				So(calls[0].args, ShouldResemble, []string{"add-generic-password", "-U", "-s", keyringService, "-a", "ALPHA", "-X", payload})
				So(calls[0].stdin, ShouldBeNil)

				So(err, ShouldBeNil)
				So(string(decoded), ShouldEqual, encodePayload(value))
				So(string(decoded), ShouldEqual, "v1:"+hex.EncodeToString([]byte(value)))

				for _, arg := range calls[0].args {
					So(arg, ShouldNotContainSubstring, value)
					So(arg, ShouldNotContainSubstring, "line1")
				}
			})
		})
	})
}

func TestKeyringSetUsesStdinOnLinux(t *testing.T) {
	Convey("Given a linux keyring", t, func() {
		var calls []keyringCall

		keyring := testShellKeyring(t, linuxTool, recordingRunner(t, &calls, func(keyringCall) ([]byte, int, error) {
			return nil, 0, nil
		}))

		value := "line1\nline2 $ecret"

		Convey("When a value is stored", func() {
			So(keyring.Set("ALPHA", value), ShouldBeNil)
			So(calls, ShouldHaveLength, 1)

			Convey("Then the payload goes over stdin and never in plaintext", func() {
				So(calls[0].args, ShouldResemble, []string{"store", "--label=" + keyringService + ": ALPHA", "service", keyringService, "account", "ALPHA"})
				So(string(calls[0].stdin), ShouldEqual, encodePayload(value))
				So(string(calls[0].stdin), ShouldContainSubstring, payloadPrefix)
				So(string(calls[0].stdin), ShouldNotContainSubstring, value)
			})
		})
	})
}

func TestKeyringLinuxRoundTripKeepsTrailingNewline(t *testing.T) {
	Convey("Given a linux keyring that stores stdin verbatim", t, func() {
		value := "line1\nline2\n"

		var stored []byte

		keyring := testShellKeyring(t, linuxTool, runnerFunc(func(_ string, args []string, stdin []byte) ([]byte, int, error) {
			if args[0] == "store" {
				stored = append([]byte(nil), stdin...)

				return nil, 0, nil
			}

			return append(append([]byte(nil), stored...), '\n'), 0, nil
		}))

		Convey("When a value ending with a newline round-trips", func() {
			So(keyring.Set("ALPHA", value), ShouldBeNil)
			So(string(stored), ShouldEqual, encodePayload(value))

			got, found, err := keyring.Get("ALPHA")

			Convey("Then the trailing newline survives", func() {
				So(err, ShouldBeNil)
				So(found, ShouldBeTrue)
				So(got, ShouldEqual, value)
			})
		})
	})
}

func TestKeyringSetFailure(t *testing.T) {
	Convey("Given a keyring whose write is denied", t, func() {
		var calls []keyringCall

		keyring := testShellKeyring(t, darwinTool, recordingRunner(t, &calls, func(keyringCall) ([]byte, int, error) {
			return nil, 1, errors.New("security: write denied")
		}))

		Convey("When a value is stored", func() {
			err := keyring.Set("ALPHA", "s3cr3t")

			Convey("Then the error is reported without the value", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "keyring set ALPHA")
				So(err.Error(), ShouldNotContainSubstring, "s3cr3t")
			})
		})
	})
}

func TestKeyringDeleteRemovesEntry(t *testing.T) {
	Convey("Given a keyring where delete succeeds and the read-back misses", t, func() {
		var calls []keyringCall

		keyring := testShellKeyring(t, darwinTool, recordingRunner(t, &calls, func(call keyringCall) ([]byte, int, error) {
			if call.args[0] == "delete-generic-password" {
				return nil, 0, nil
			}

			return nil, darwinMissing, exitError()
		}))

		Convey("When the entry is deleted", func() {
			removed, err := keyring.Delete("ALPHA")

			Convey("Then delete is followed by a read-back", func() {
				So(err, ShouldBeNil)
				So(removed, ShouldBeTrue)
				So(calls, ShouldHaveLength, 2)
				So(calls[0].args, ShouldResemble, []string{"delete-generic-password", "-s", keyringService, "-a", "ALPHA"})
				So(calls[1].args[0], ShouldEqual, "find-generic-password")
			})
		})
	})
}

func TestKeyringDeleteNotFound(t *testing.T) {
	Convey("Given a keyring where the entry is already missing", t, func() {
		var calls []keyringCall

		keyring := testShellKeyring(t, darwinTool, recordingRunner(t, &calls, func(keyringCall) ([]byte, int, error) {
			return nil, darwinMissing, exitError()
		}))

		Convey("When the entry is deleted", func() {
			removed, err := keyring.Delete("ALPHA")

			Convey("Then absence is reported without error", func() {
				So(err, ShouldBeNil)
				So(removed, ShouldBeFalse)
			})
		})
	})
}

func TestKeyringDeleteReadBackProof(t *testing.T) {
	Convey("Given a keyring whose entry survives delete", t, func() {
		var calls []keyringCall

		keyring := testShellKeyring(t, darwinTool, recordingRunner(t, &calls, func(call keyringCall) ([]byte, int, error) {
			if call.args[0] == "delete-generic-password" {
				return nil, 0, nil
			}

			return []byte("still here\n"), 0, nil
		}))

		Convey("When the entry is deleted", func() {
			removed, err := keyring.Delete("ALPHA")

			Convey("Then the read-back reports it still present", func() {
				So(err, ShouldBeError)
				So(err.Error(), ShouldContainSubstring, "still present")
				So(removed, ShouldBeFalse)
			})
		})
	})
}

func TestKeyringGetReturnsForeignRawValue(t *testing.T) {
	Convey("Given a keyring item added outside beadle", t, func() {
		var calls []keyringCall

		keyring := testShellKeyring(t, darwinTool, recordingRunner(t, &calls, func(keyringCall) ([]byte, int, error) {
			return []byte("hand-added password\n"), 0, nil
		}))

		Convey("When it is fetched", func() {
			value, found, err := keyring.Get("ALPHA")

			Convey("Then the raw value is returned as-is", func() {
				So(err, ShouldBeNil)
				So(found, ShouldBeTrue)
				So(value, ShouldEqual, "hand-added password")
			})
		})
	})
}

func TestKeyringGetRejectsCorruptPayload(t *testing.T) {
	Convey("Given a keyring holding a corrupt payload", t, func() {
		var calls []keyringCall

		keyring := testShellKeyring(t, darwinTool, recordingRunner(t, &calls, func(keyringCall) ([]byte, int, error) {
			return []byte(payloadPrefix + "zz\n"), 0, nil
		}))

		Convey("When it is fetched", func() {
			_, _, err := keyring.Get("ALPHA")

			Convey("Then the corrupt payload is rejected", func() {
				So(errors.Is(err, errCorruptPayload), ShouldBeTrue)
			})
		})
	})
}

func TestKeyringLookPathIsLazy(t *testing.T) {
	Convey("Given a keyring whose tool is missing", t, func() {
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

		Convey("When get, set and delete are called", func() {
			_, _, err := keyring.Get("ALPHA")
			So(errors.Is(err, ErrKeyringUnavailable), ShouldBeTrue)
			So(err.Error(), ShouldContainSubstring, darwinTool)

			So(errors.Is(keyring.Set("ALPHA", "s3cr3t"), ErrKeyringUnavailable), ShouldBeTrue)

			_, err = keyring.Delete("ALPHA")

			Convey("Then the tool is never executed", func() {
				So(errors.Is(err, ErrKeyringUnavailable), ShouldBeTrue)
				So(called, ShouldEqual, 0)
			})
		})
	})
}

func TestKeyringSupportedPlatforms(t *testing.T) {
	Convey("Given the host platform", t, func() {
		Convey("When a shell keyring is constructed", func() {
			keyring, err := NewShellKeyring(ExecRunner{})

			Convey("Then it is supported on darwin and linux only", func() {
				switch runtime.GOOS {
				case "darwin", "linux":
					So(err, ShouldBeNil)
					So(keyring, ShouldNotBeNil)
				default:
					So(errors.Is(err, ErrKeyringUnsupported), ShouldBeTrue)
				}
			})
		})
	})
}
