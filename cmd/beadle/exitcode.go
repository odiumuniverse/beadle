package main

import (
	"errors"

	"github.com/odiumuniverse/beadle/pkg/agent"
	"github.com/odiumuniverse/beadle/pkg/cli"
	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/hostcli"
	"github.com/odiumuniverse/beadle/pkg/lock"
	"github.com/odiumuniverse/beadle/pkg/secret"
	"github.com/odiumuniverse/beadle/pkg/state"
	"github.com/odiumuniverse/verger/pkg/apply"
	"github.com/odiumuniverse/verger/pkg/consent"
	"github.com/odiumuniverse/verger/pkg/exitcode"
	vergerlock "github.com/odiumuniverse/verger/pkg/lock"
	"github.com/odiumuniverse/verger/pkg/spec"
	"github.com/odiumuniverse/verger/pkg/store"
	"github.com/odiumuniverse/verger/pkg/verger"
)

// classify maps a beadle error to the shared exit class, or exitcode.OK for a
// nil error. It works through wrapping, so a command may return
// fmt.Errorf("...: %w", err) without losing the class.
//
// The classes themselves come from github.com/odiumuniverse/verger/pkg/exitcode:
// beadle depends on verger, so the integers are declared once and a script that
// runs either tool reads the same number for the same situation. This function
// holds only beadle's half of the mapping, because only beadle knows what its
// own error types mean.
func classify(err error) int {
	if err == nil {
		return exitcode.OK
	}

	switch {
	// 4 — policy refusal. engine.IsRefusal is the only beadle error designed
	// to be classified: it carries a machine code and is found through
	// wrapping, so a command may return fmt.Errorf("...: %w", refusal)
	// without losing the class. Checked first because a refusal is a rule
	// speaking, and a rule outranks whatever state it produced.
	case isRefusal(err):
		return exitcode.Policy

	// 7 — schema newer. A vault document written by a newer beadle is never
	// rewritten by an older one: beadle cannot know what the newer fields
	// mean, and overwriting them would destroy the newer build's work. So the
	// only correct answer is to stop and say "upgrade beadle" — which is a
	// different script's next move than any other class here, and the reason
	// it gets a number of its own instead of falling through to
	// exitcode.Unexpected — "a bug: something the tool did not anticipate" —
	// at the bottom. Placed above the classes below because they describe the
	// invocation and the host, and none of them is worth acting on while the
	// file cannot be read at all; below the refusal, because a rule speaking
	// outranks whatever state it was reading. verger's own four typed
	// schema-newer errors land here too, so a beadle run that reaches the
	// library reaches the same class.
	case isSchemaNewer(err):
		return exitcode.SchemaNewer

	// 5 — consent. Two kinds of it, and the second one arrived with verger's
	// channel work: a secret cannot be stored without a keyring, and only the
	// user can provide one; and a question the tool could not put to the user
	// is consent nobody gave. verger classifies both the same way, on the
	// grounds that the user's next move in either case is to answer something
	// or pass -y, and calling it a conflict told a script the run had failed
	// for a reason it cannot act on. Matching that mapping is the point of
	// sharing the table, so the sentinel beadle had no name for is named here.
	// v0.1.2's PendingConsentError arrives here on that last arm, and it is
	// worth being exact about how, because the obvious reading is wrong.
	// *verger.PendingConsentError carries an Is method that reports itself
	// equal to apply.ErrConfirmationRequired (pkg/verger/exec.go), so errors.Is
	// matches it above. A second arm naming the type explicitly would sit below
	// this one and could never fire — dead code in a switch whose whole promise
	// is that a script can branch on the number. Such an arm was written here,
	// and removed for exactly that reason.
	//
	// What stops the class from being lost if the library ever drops that alias
	// is the test, not the branch: TestPendingConsentIsConsentThroughEvery
	// Wrapping asserts the number directly and goes red when the alias goes.
	case errors.Is(err, secret.ErrKeyringUnavailable),
		errors.Is(err, secret.ErrKeyringUnsupported),
		errors.Is(err, apply.ErrConfirmationRequired):
		return exitcode.Consent

	// 3 — conflict. Another beadle process holds the vault. That is ordinary,
	// not a crash: the user either waits or stops the other process.
	case errors.Is(err, lock.ErrBusy):
		return exitcode.Conflict

	// 3 — conflict, the other kind. Open conflicts are a decision the user has
	// to make, not a crash and not a policy refusal: beadle did what it could
	// and stopped where the user's own files were involved. A script's next
	// move is the same as for a busy lock — settle it, then run again — so it
	// is the same class.
	case isOpenConflicts(err):
		return exitcode.Conflict

	// 3 — conflict, the kind v0.1.2 made explicit. verger's HandsOffError is a
	// cell it refused to write because the file on disk is the user's own: the
	// package is fine, nothing is broken, and the way out is theirs to choose
	// (`--force` keeps their copy and overwrites). The library classifies it as a
	// conflict for the same reason beadle does here — a script that reads "done"
	// over a file that was deliberately left alone is the failure this number
	// exists to prevent, so it must not fall through to Unexpected.
	case isHandsOffConflict(err):
		return exitcode.Conflict

	// 6 — host unavailable. ErrNotFound means the host CLI is not installed.
	case errors.Is(err, hostcli.ErrNotFound):
		return exitcode.HostUnavailable

	// 2 — usage. A vault that does not exist yet means the user has not run
	// `beadle init`: a missing step, not a crash. beadle already says so in
	// plain words, so classifying it as unexpected would report a routine
	// situation as a fault.
	case errors.Is(err, cli.ErrVaultNotInitialized):
		return exitcode.Usage

	// 2 — usage. The user named an agent that has no config.
	case errors.Is(err, agent.ErrNotConfigured):
		return exitcode.Usage

	// 2 — usage, from cobra. pkg/cli wraps every flag and argument error in
	// verger's typed UsageError, so this matches the type and never the
	// message. Checked after the cases above because a more specific class
	// outranks a generic one.
	case isUsageError(err):
		return exitcode.Usage

	default:
		return exitcode.Unexpected
	}
}

// isRefusal reports whether err is, or wraps, a policy refusal.
func isRefusal(err error) bool {
	_, ok := engine.IsRefusal(err)

	return ok
}

// isSchemaNewer reports whether err is, or wraps, a persisted document
// written by a newer tool. Both halves are named: beadle's own config.json
// and state.json, and verger's consent/store/lock/spec, which beadle reaches
// through the library. Every document that can carry a schema is listed, so
// the class does not depend on which command happened to read it — that is
// the whole point of a number a script can branch on.
func isSchemaNewer(err error) bool {
	if _, beadleConfig := config.IsSchemaNewer(err); beadleConfig {
		return true
	}

	if _, beadleState := state.IsSchemaNewer(err); beadleState {
		return true
	}

	_, vergerSpec := errors.AsType[*spec.SchemaNewerError](err)
	_, vergerConsent := errors.AsType[*consent.SchemaNewerError](err)
	_, vergerStore := errors.AsType[*store.SchemaNewerError](err)
	_, vergerLock := errors.AsType[*vergerlock.SchemaNewerError](err)

	return vergerSpec || vergerConsent || vergerStore || vergerLock
}

// isOpenConflicts reports whether err is, or wraps, open conflicts.
func isOpenConflicts(err error) bool {
	_, ok := engine.IsOpenConflicts(err)

	return ok
}

// isHandsOffConflict reports whether err is, or wraps, verger's hands-off
// refusal: a cell the run left alone because the file on disk is the user's own.
// It is a conflict and not a failure, and the type is read rather than the
// message, so a rewording cannot change the class.
func isHandsOffConflict(err error) bool {
	_, ok := errors.AsType[*verger.HandsOffError](err)

	return ok
}

// refusalCode returns the stable machine code of a policy refusal, or "" when
// err is not one. The code is the part a script should branch on — never the
// message — and it is why engine.RefusalError grew a Code() accessor. Every code
// maps to the same exit class, which is why classify needs only the boolean.
func refusalCode(err error) string {
	refused, ok := engine.IsRefusal(err)
	if !ok {
		return ""
	}

	return refused.Code()
}

// isUsageError reports whether err is, or wraps, a usage error — a flag the
// user got wrong or the wrong number of arguments. pkg/cli puts one around
// every such error, so this reads the type and never the message.
func isUsageError(err error) bool {
	_, ok := errors.AsType[*verger.UsageError](err)

	return ok
}
