package cli

import "github.com/spf13/cobra"

// NewRootCmd returns the beadle command tree for a caller that needs to walk
// it rather than execute it — cmd/gendocs renders the man pages from it, so
// the pages and the binary cannot disagree about which flags exist.
//
// It exists as its own file, delegating to the unexported constructor, so the
// internal call sites are untouched and this stays an addition rather than an
// edit to a file another worker owns. The signature matches verger's, so the
// two generators are the same shape.
func NewRootCmd(opts Options) *cobra.Command {
	return newRootCmd(opts)
}
