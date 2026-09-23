package engine

import (
	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/hooks"
	"github.com/odiumuniverse/beadle/pkg/secret"
)

func SetBundlesRunnerForTest(runner secret.Runner) func() {
	previous := bundlesRunner
	bundlesRunner = runner

	return func() { bundlesRunner = previous }
}

func SetBundlesLookPathForTest(lookPath func(string) (string, error)) func() {
	previous := bundlesLookPath
	bundlesLookPath = lookPath

	return func() { bundlesLookPath = previous }
}

func SetDaemonStatusRunnerForTest(runner secret.Runner) func() {
	previous := daemonStatusRunner
	daemonStatusRunner = runner

	return func() { daemonStatusRunner = previous }
}

// SetHookPresentSeamForTest interleaves a file change between the hooks plan
// and the write.
func SetHookPresentSeamForTest(seam func(string)) func() {
	previous := hookPresentSeam
	hookPresentSeam = seam

	return func() { hookPresentSeam = previous }
}

// RenderableHooksForTest exposes the hooks one bundle host renders.
func RenderableHooksForTest(e *Engine, host string, canon map[string]hooks.Hook) map[string]hooks.Hook {
	parsed, err := bundle.ParseHost(host)
	if err != nil {
		return nil
	}

	return e.renderableHooks(parsed, canon)
}

// BundleRequestHooksForTest exposes the hooks one bundle host would receive.
func BundleRequestHooksForTest(e *Engine, host string) (map[string]hooks.Hook, error) {
	parsed, err := bundle.ParseHost(host)
	if err != nil {
		return nil, err
	}

	request, _, err := e.bundleRequest(parsed)

	return request.Hooks, err
}

// WarnKeptCopiesForTest exposes the kept-copy summary for a host.
func WarnKeptCopiesForTest(host string, kept []string) []string {
	report := &Report{}
	warnKeptCopies(bundle.Host(host), kept, report)

	return report.Warnings
}
