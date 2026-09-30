package main

import (
	"fmt"
	"os"

	"github.com/odiumuniverse/beadle/pkg/cli"
	"github.com/odiumuniverse/verger/pkg/watchbind"
)

var version = "dev"

func main() {
	// The plugin library declares a watch contract and owns no engine, so the
	// binary is where the two are bound - through verger's own watchbind, the
	// same call verger's own main makes. Without it the library reports the
	// capability as unavailable and `beadle watch` says "watch is not
	// available" instead of watching anything.
	watchbind.Install()

	if err := cli.Execute(cli.Options{Version: version}); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		// The status is a class, not a boolean: a conflict the user must
		// settle is not a crash. The classes themselves come from verger, so
		// both tools report the same number for the same situation.
		os.Exit(classify(err))
	}
}
