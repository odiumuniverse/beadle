package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/engine"
	"github.com/odiumuniverse/beadle/pkg/state"
	"github.com/odiumuniverse/beadle/pkg/vault"
	"github.com/odiumuniverse/verger/pkg/apply"
	"github.com/odiumuniverse/verger/pkg/verger"
)

func (a *app) newBundlesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bundles",
		Short: "Generate and register native host bundles (Claude marketplace, Gemini extension, Antigravity plugin, omp marketplace)",
		Args:  cobra.NoArgs,
	}

	cmd.AddCommand(a.newBundlesStatusCmd(), a.newBundlesToggleCmd(true), a.newBundlesToggleCmd(false))

	return cmd
}

func (a *app) newBundlesStatusCmd() *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show bundle registration per host",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			v, _, err := a.loadConfig()
			if err != nil {
				return err
			}

			st, err := state.Load(v.StatePath())
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()

			if asJSON {
				return writeJSON(out, newBundlesDocument(bundleStates(st)))
			}

			fmt.Fprintf(out, "  %-13s %-8s %-11s %-16s %-14s %s\n", "host", "enabled", "registered", "version", "tier", "cli")

			for _, host := range bundle.Hosts() {
				entry := st.Bundles[string(host)]

				version := entry.Version
				if version == "" {
					version = "-"
				}

				tier := entry.VerifyTier
				if tier == "" {
					tier = "-"
				}

				cli := "missing"
				if _, err := exec.LookPath(host.Binary()); err == nil {
					cli = "found"
				}

				fmt.Fprintf(out, "  %-13s %-8s %-11s %-16s %-14s %s\n", host, onOff(entry.Enabled, "yes", "no"), onOff(entry.Registered, "yes", "no"), version, tier, cli)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "print the document as JSON")

	return cmd
}

// newBundlesToggleCmd is `beadle bundles enable|disable`: beadle renders the
// canon bundle and registers it with the host CLI. It is the OLD path and it
// still works; the canon-as-a-package path lives at `beadle plugins canon` and
// replaces it once the old registration is retired, so the two never both claim
// the same host files.
func (a *app) newBundlesToggleCmd(enable bool) *cobra.Command {
	use, short := "disable", "Unregister the bundle and restore file sync"
	if enable {
		use, short = "enable", "Generate the bundle and register it with the host CLI when available"
	}

	var host string

	cmd := &cobra.Command{
		Use:   use + " [host]",
		Short: short,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host, err := bundleHostArg(args, host)
			if err != nil {
				return err
			}

			e, err := a.engine()
			if err != nil {
				return err
			}

			var report engine.Report

			if enable {
				report, err = e.BundlesEnable(cmd.Context(), host)
			} else {
				report, err = e.BundlesDisable(cmd.Context(), host)
			}

			if err != nil {
				return err
			}

			printBundleReport(cmd, report)

			return nil
		},
	}

	cmd.Flags().StringVar(&host, "host", "", "bundle host: claude, gemini, antigravity or omp (positional works too)")

	return cmd
}

// bundleHostArg accepts the host positionally (the form every hint prints) or
// through --host; the two must not disagree.
func bundleHostArg(args []string, host string) (string, error) {
	if len(args) == 0 {
		if host == "" {
			return "", errors.New("host is required: beadle bundles enable claude")
		}

		return host, nil
	}

	if host != "" && host != args[0] {
		return "", fmt.Errorf("host %q does not match --host %q", args[0], host)
	}

	return args[0], nil
}

func printBundleReport(cmd *cobra.Command, report engine.Report) {
	out := cmd.OutOrStdout()

	for _, line := range bundleLines(report.Bundles) {
		fmt.Fprintln(out, line)
	}

	for _, warning := range report.Warnings {
		fmt.Fprintln(out, "  ! "+warning)
	}
}

// pluginEvents is the progress sink the plugin manager writes to; it is stderr,
// because stdout is the command's document.
func (a *app) pluginEvents(cmd *cobra.Command) chan<- verger.Event {
	events := make(chan verger.Event, 64)

	go renderPluginEvents(cmd.ErrOrStderr(), events)

	return events
}

// renderCanonPackageForCLI renders the canon package without running a whole
// sync, so `bundles enable` on a quiet vault has something to install.
func (a *app) renderCanonPackageForCLI(ctx context.Context, v *vault.Vault) error {
	e, err := a.engine()
	if err != nil {
		return err
	}

	return e.RenderCanonPackage(ctx)
}

// bundleReportFrom turns the plugin manager's apply report into the engine
// report the old bundle commands printed, so the output shape a user reads
// does not change with the mechanism behind it.
func bundleReportFrom(report *apply.Report, note string) engine.Report {
	out := engine.Report{Notes: []string{note}}

	for _, cell := range report.Cells {
		out.Notes = append(out.Notes, fmt.Sprintf("your configuration %s %s %s", cell.Package, cell.Host, cell.Status))
	}

	return out
}

// canonPackageToggle is the body of `beadle bundles enable|disable` after the
// move: the canon package is written to the plugin manager's spec and then
// installed (or removed) through the embedded client. With no host named it
// goes to every host the manager can deliver to, which is what the old
// all-hosts bundle attempt did.
func (a *app) canonPackageToggle(cmd *cobra.Command, host string, enable bool) error {
	v, _, err := a.loadConfig()
	if err != nil {
		return err
	}

	client, err := a.pluginClient(v, a.pluginEvents(cmd))
	if err != nil {
		return err
	}

	defer func() { _ = client.Close() }()

	var hosts []string
	if host != "" {
		hosts = []string{host}
	}

	ctx := cmd.Context()

	if !enable {
		report, err := client.RemoveCanonPackage(ctx)
		if err != nil {
			return err
		}

		printBundleReport(cmd, bundleReportFrom(report, "your configuration removed"))

		return nil
	}

	// The canon is rendered by the sync; installing before it exists would
	// install an empty package, so the tree is rendered first.
	if err := a.renderCanonPackageForCLI(ctx, v); err != nil {
		return err
	}

	report, err := client.InstallCanonPackage(ctx, v.CanonPackageDir(), hosts)
	if err != nil {
		return err
	}

	// The canon package supersedes beadle's own directory marketplace on
	// every host it went to. The registration was the library's, so its
	// removal goes through the library's inverse — never by editing a host
	// registry by hand.
	if len(hosts) == 0 {
		hosts = installedHosts(report)
	}

	for _, hostID := range hosts {
		if err := client.UnregisterHost(ctx, hostID, oldMarketplaceName); err != nil {
			return err
		}
	}

	printBundleReport(cmd, bundleReportFrom(report, "your configuration installed"))

	return nil
}

// oldMarketplaceName is the directory marketplace beadle registered itself,
// which the canon package replaces.
const oldMarketplaceName = "beadle"

// installedHosts is the hosts an apply report actually delivered to, in a
// stable order.
func installedHosts(report *apply.Report) []string {
	var hosts []string

	for _, cell := range report.Cells {
		hosts = append(hosts, string(cell.Host))
	}

	slices.Sort(hosts)

	return slices.Compact(hosts)
}
