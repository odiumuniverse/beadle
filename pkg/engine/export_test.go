package engine

import "github.com/odiumuniverse/beadle/pkg/secret"

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
