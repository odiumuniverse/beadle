package main

import (
	"fmt"
	"os"

	"github.com/odiumuniverse/agents-sync/pkg/cli"
)

var version = "dev"

func main() {
	if err := cli.Execute(cli.Options{Version: version}); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
