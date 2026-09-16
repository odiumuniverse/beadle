package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/odiumuniverse/agents-sync/pkg/fsutil"
	"github.com/odiumuniverse/agents-sync/pkg/skill"
)

const (
	stubMarkerVersion = "v1"
	stubScriptName    = "stub"
)

var errUnsafeStubInput = errors.New("unsafe plugin stub input")

func stubInputsSafe(name, key, version string) bool {
	return !unsafeStubValue(name) && !unsafeStubValue(key) && !unsafeStubValue(version) && filepath.IsLocal(name)
}

func unsafeStubValue(value string) bool {
	if value == "" {
		return true
	}

	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune("'\"`$\\", r) {
			return true
		}
	}

	return false
}

func writePluginStub(dir, name, key, version string, at time.Time) (bool, error) {
	if !stubInputsSafe(name, key, version) {
		return false, errUnsafeStubInput
	}

	if _, err := os.Lstat(dir); err == nil {
		return false, nil
	}

	date := at.Format(time.RFC3339)
	message := fmt.Sprintf("This skill was provided by the plugin %s@%s, which is no longer installed (since %s). Run agent-sync heal to clean up, or reinstall the plugin.",
		key, version, date)

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return false, err
	}

	marker := fmt.Sprintf("%s %s %s %s\n", stubMarkerVersion, key, version, date)
	if err := fsutil.WriteFileAtomic(filepath.Join(dir, skill.StubMarkerFile), []byte(marker), 0o644); err != nil {
		return false, err
	}

	doc := "---\nname: " + strconv.Quote(name) + "\ndescription: unavailable plugin stub\n---\n\n" + message + "\n"
	if err := fsutil.WriteFileAtomic(filepath.Join(dir, farmSkillFile), []byte(doc), 0o644); err != nil {
		return false, err
	}

	script := "#!/bin/sh\nprintf '%s\\n' '" + strings.ReplaceAll(message, "'", `'\''`) + "'\nexit 1\n"
	if err := fsutil.WriteFileAtomic(filepath.Join(dir, stubScriptName), []byte(script), 0o755); err != nil {
		return false, err
	}

	return true, nil
}
