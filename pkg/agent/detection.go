package agent

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// NeverProbed is the reason for a host with no verified binary and no config
// marker: the adapter has never been live-probed, so the screen must not claim
// it looked and found nothing.
const NeverProbed = "never live-probed on this machine"

// Detection is *why* an agent is or is not on this machine, not just whether.
//
// `Detect` answers a yes/no that onboarding then has to render as one generic
// "not found", which leaves a user unable to tell the two very different cases
// apart: the CLI is not installed, or it is installed and its config directory
// is missing. Those have different fixes, and the spec (W7-UX §5.3) asks for
// the reason to be on the line.
type Detection struct {
	// Found is the same answer Detect gives. It is repeated here so a caller
	// that has a DetectReason does not have to call both and risk disagreeing
	// with itself.
	Found bool

	// Reason is the found-case detail: what was found, e.g. "found at ~/.claude".
	Reason string

	// Missing is the not-found detail: what is absent, phrased as the thing
	// the user must do, e.g. "claude is not on PATH". Empty when Found.
	Missing string
}

// Line renders one agent row for the onboarding screen.
func (d Detection) Line() string {
	if d.Found {
		if d.Reason == "" {
			return "detected"
		}

		return d.Reason
	}

	if d.Missing == "" {
		return "not found"
	}

	return d.Missing
}

// detectWithReason runs a detector and, when it says no, asks the binaries
// whether the CLI is the thing that is missing. That ordering matters: a host
// with a config directory but no CLI is *present*, and reporting it as "not
// installed" would send a user to install something they already have.
//
// The fallback is deliberate rather than a second registry: `Detect` stays the
// single source of truth for "is it there", and the reason only ever explains a
// negative.
func detectWithReason(detect func() (bool, error), binaries []string, configPath string) (Detection, error) {
	found, err := detect()
	if err != nil {
		return Detection{}, err
	}

	if found {
		if configPath != "" {
			return Detection{Found: true, Reason: "found at " + configPath}, nil
		}

		return Detection{Found: true, Reason: "detected"}, nil
	}

	// Not found. Name the missing half.
	if configPath != "" {
		return Detection{Found: false, Missing: "no config at " + configPath}, nil
	}

	if len(binaries) > 0 {
		return Detection{Found: false, Missing: binaryMissing(binaries)}, nil
	}

	// No binary was ever verified for this host on any platform, so the
	// honest line says that rather than "not installed", which is a claim
	// about the machine that nobody can check.
	return Detection{Found: false, Missing: NeverProbed}, nil
}

// binaryMissing names the binaries that are not on PATH, or says that one is.
func binaryMissing(binaries []string) string {
	var absent []string

	for _, binary := range binaries {
		if _, err := exec.LookPath(binary); err != nil {
			absent = append(absent, binary)
		}
	}

	if len(absent) == 0 {
		return "installed, but no config found"
	}

	return strings.Join(absent, ", ") + " is not on PATH"
}

// ConfigDirOf is the path a config-based detector looks at, taken from the
// agent's own first surface. It is what lets the reason say "no config at
// ~/.claude" instead of falling through to the no-marker branch, which would
// tell a user with claude installed to install something they have.
func ConfigDirOf(a *Agent) string {
	if a == nil {
		return ""
	}

	for _, s := range a.Surfaces {
		if p := s.Path(); p != "" {
			return filepath.Dir(p)
		}
	}

	return ""
}

// DetectReasonOf returns the detection for an agent, preferring its own
// DetectReason and falling back to Detect. A nil DetectFunc with no reason
// yields a negative that says so, rather than a panic.
func DetectReasonOf(a *Agent, home, configPath string) Detection {
	if configPath == "" {
		configPath = ConfigDirOf(a)
	}

	if a == nil {
		return Detection{Missing: "no detector"}
	}

	if a.DetectReason != nil {
		d, err := a.DetectReason()
		if err == nil {
			return d
		}

		return Detection{Missing: "could not be checked: " + err.Error()}
	}

	if a.Detect == nil {
		return Detection{Missing: "cannot be detected here"}
	}

	d, err := detectWithReason(a.Detect, a.Binaries, configPath)
	if err != nil {
		return Detection{Missing: "could not be checked: " + err.Error()}
	}

	return d
}
