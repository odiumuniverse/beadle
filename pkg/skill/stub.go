package skill

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

const StubMarkerFile = ".agent-sync-quarantine"

func IsStubDir(dir string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(dir, StubMarkerFile)) //nolint:gosec // G304: the caller resolves the directory
	if err != nil {
		return "", false
	}

	fields := strings.Fields(string(data))
	if len(fields) != 4 || fields[0] != "v1" {
		return "", false
	}

	if _, err := time.Parse(time.RFC3339, fields[3]); err != nil {
		return "", false
	}

	return fields[1], true
}
