package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/hostcli"
)

// hostRunner runs a resolved host CLI; tests replace it.
type hostRunner interface {
	Run(bin hostcli.Binary, args []string, stdin []byte) (stdout []byte, code int, err error)
}

// execHostRunner runs the binary for real: a host that exits non-zero returns
// its code, and a CLI that vanished keeps hostcli.ErrNotFound in the chain.
type execHostRunner struct{}

func (execHostRunner) Run(bin hostcli.Binary, args []string, stdin []byte) ([]byte, int, error) {
	stdout, err := bin.Run(context.Background(), args, stdin)
	if exitErr, ok := errors.AsType[*hostcli.ExitError](err); ok {
		return stdout, exitErr.Code, err
	}

	if err != nil {
		return stdout, -1, err
	}

	return stdout, 0, nil
}

// hostCLI resolves a bundle host's CLI: the process PATH first, then the
// location an attended run recorded, so the watcher a service manager starts
// without the user's shell PATH still reaches it. The records file is read on
// every call, and an unreadable one counts as no records.
func (e *Engine) hostCLI(host bundle.Host) (hostcli.Binary, error) {
	records, err := hostcli.LoadRecords(e.vault.HostCLIPath())
	if err != nil {
		records = nil
	}

	return hostcli.NewResolver(hostcli.WithLookPath(bundlesLookPath), hostcli.WithRecords(records)).Resolve(host.Binary())
}

// runHost resolves the host CLI and runs one command.
func (e *Engine) runHost(host bundle.Host, args ...string) ([]byte, int, error) {
	bin, err := e.hostCLI(host)
	if err != nil {
		return nil, -1, err
	}

	return bundlesRunner.Run(bin, args, nil)
}

// recordHostCLIs remembers where this attended run finds each host CLI, with
// its PATH, so an unattended run reaches the same binary. Only a PATH hit is
// recorded; the watcher and dry runs never write. A record that cannot be
// saved is a warning: the run itself still has the CLI.
func (e *Engine) recordHostCLIs() []string {
	if e.unattended {
		return nil
	}

	path := e.vault.HostCLIPath()

	records, err := hostcli.LoadRecords(path)
	if err != nil {
		records = hostcli.Records{}
	}

	env := os.Getenv("PATH")
	changed := false

	for _, host := range bundle.Hosts() {
		name := host.Binary()

		found, err := bundlesLookPath(name)
		if err != nil {
			continue
		}

		if abs, err := filepath.Abs(found); err == nil {
			found = abs
		}

		if previous, ok := records[name]; ok && previous.Path == found && previous.PATH == env {
			continue
		}

		records[name] = hostcli.Record{Path: found, PATH: env, At: e.now().UTC()}
		changed = true
	}

	if !changed {
		return nil
	}

	if err := records.Save(path); err != nil {
		return []string{"bundles: " + err.Error()}
	}

	return nil
}
