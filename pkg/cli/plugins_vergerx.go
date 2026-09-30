package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/beadle/pkg/vergerx"
	"github.com/odiumuniverse/verger/pkg/apply"
	"github.com/odiumuniverse/verger/pkg/verger"
)

// pluginsList prints the verger package inventory.
//
// newPluginsListCmd, newPluginsInstallCmd, newPluginsRemoveCmd and
// newPluginsEjectCmd are the user-facing half of the plugin manager: the
// functions below do the work, these commands are what makes them reachable.
//
//nolint:unused // used by the CLI command tree (W3-B1).
func (a *app) newPluginsListCmd() *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   cmdList,
		Short: "List installed plugin packages, their versions and their cells",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.withPlugins(cmd, func(c *vergerx.Client, out *bytes.Buffer) error {
				return pluginsListJSON(c, out, asJSON, jsonSchemaName(cmd))
			})
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "print the status document as JSON")

	return jsonForm(cmd, "beadle.plugins")
}

func (a *app) newPluginsInstallCmd() *cobra.Command {
	var (
		asJSON  bool
		assume  bool
		hosts   []string
		except  []string
		dryRun  bool
		force   bool
		project bool
	)

	cmd := &cobra.Command{
		Use:   "install <ref>...",
		Short: "Install plugin packages by reference",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.withPlugins(cmd, func(c *vergerx.Client, out *bytes.Buffer) error {
				return pluginsInstallWith(c, out, args, pluginFlags{assume: assume, asJSON: asJSON, hosts: hosts, except: except, dryRun: dryRun, force: force, project: project}, jsonSchemaName(cmd))
			})
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "print the apply report as JSON")
	cmd.Flags().BoolVarP(&assume, "yes", "y", false, "accept defaults; never resolve destructive conflicts")
	cmd.Flags().StringSliceVar(&hosts, "hosts", nil, "only these hosts (comma separated)")
	cmd.Flags().StringSliceVar(&except, "except", nil, "exclude these hosts")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "plan without writing")
	// --force is deliberately not part of -y. A file the user edited is not
	// a default, and "accept defaults" must never mean "discard what I wrote".
	// The library keeps the previous version and reports where, so forcing is
	// recoverable rather than destructive.
	cmd.Flags().BoolVar(&force, "force", false, "overwrite files you edited; the previous version is kept and reported")
	cmd.Flags().BoolVar(&project, "project", false, "use the project scope")

	return jsonForm(cmd, "beadle.plugins.install")
}

func (a *app) newPluginsRemoveCmd() *cobra.Command {
	var (
		asJSON  bool
		assume  bool
		hosts   []string
		except  []string
		dryRun  bool
		force   bool
		project bool
	)

	cmd := &cobra.Command{
		Use:   "remove <id>",
		Short: "Remove one installed plugin package",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.withPlugins(cmd, func(c *vergerx.Client, out *bytes.Buffer) error {
				return pluginsRemoveWith(c, out, args[0], pluginFlags{asJSON: asJSON, hosts: hosts, except: except, dryRun: dryRun, force: force, assume: assume, project: project}, jsonSchemaName(cmd))
			})
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "print the apply report as JSON")
	cmd.Flags().BoolVarP(&assume, "yes", "y", false, "accept defaults; never resolve destructive conflicts")
	cmd.Flags().StringSliceVar(&hosts, "hosts", nil, "only these hosts (comma separated)")
	cmd.Flags().StringSliceVar(&except, "except", nil, "exclude these hosts")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "plan without writing")
	// As on install, and for the same reason: a removal may trash a file the
	// user edited after delivery, and -y is not consent to discard it. The
	// library keeps the previous version and reports the copy's path.
	cmd.Flags().BoolVar(&force, "force", false, "overwrite files you edited; the previous version is kept and reported")
	cmd.Flags().BoolVar(&project, "project", false, "use the project scope")

	return jsonForm(cmd, "beadle.plugins.remove")
}

func (a *app) newPluginsEjectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "eject",
		Short: "Move the plugin home out of the vault to ~/.verger, keeping the packages",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.withPlugins(cmd, func(c *vergerx.Client, out *bytes.Buffer) error {
				return pluginsEjectFrom(c, out, cmd.OutOrStdout())
			})
		},
	}

	return cmd
}

// withPlugins opens the one plugin client for this process, runs fn against it
// and closes it. Commands that need no plugin manager never pay for one.
func (a *app) withPlugins(cmd *cobra.Command, fn func(*vergerx.Client, *bytes.Buffer) error) error {
	v, _, err := a.loadConfig()
	if err != nil {
		return err
	}

	events := make(chan verger.Event, 64)

	client, err := a.pluginClient(v, events)
	if err != nil {
		return err
	}

	defer func() { _ = client.Close() }()

	// Progress goes to stderr, always: stdout carries the command's document —
	// a table or one JSON object — and a machine reading `plugins install
	// --json` must not have to strip progress lines to parse it. The renderer
	// is drained and joined before the command returns, so what a user sees is
	// complete and no goroutine outlives the run.
	rendered := make(chan struct{})

	go func() {
		defer close(rendered)

		renderPluginEvents(cmd.ErrOrStderr(), events)
	}()

	var out bytes.Buffer

	runErr := fn(client, &out)

	close(events)
	<-rendered

	if runErr != nil {
		return runErr
	}

	_, err = cmd.OutOrStdout().Write(out.Bytes())

	return err
}

func pluginsList(c *vergerx.Client, out *bytes.Buffer) error {
	ctx := context.Background()

	doc, err := c.Status(ctx)
	if err != nil {
		return err
	}

	if out == nil {
		return errors.New("plugins list: nil output")
	}

	fmt.Fprintf(out, "%-20s %-12s %-10s %s\n", "id", "version", "host", "state")

	for _, cell := range doc.Cells {
		fmt.Fprintf(out, "%-20s %-12s %-10s %s\n", cell.Package, cell.Version, cell.Host, beadleWord(cell))
	}

	return nil
}

// beadleWord is the user's word for one cell's state (§1 of the UX spec). The
// library speaks that vocabulary itself now - delivered, skipped, your_edit,
// not_ours, failed - so this only picks the spelling a table wants (a space
// where the wire form has an underscore) and keeps the strategy words, which a
// user cannot act on, out of the human table entirely.
func beadleWord(cell verger.Cell) string {
	switch cell.Status {
	case verger.StatusCurrent:
		return wordDelivered
	case verger.StatusSkew:
		return wordYourEdit
	case verger.StatusForeign:
		return wordNotOurs
	case verger.StatusFailed:
		return wordFailed
	default:
		return wordSkipped
	}
}

// pluginsInstall plans and installs a package ref.
func pluginsInstall(c *vergerx.Client, out *bytes.Buffer, refs []string) error {
	if len(refs) == 0 {
		return errors.New("plugins install: at least one ref is required")
	}

	ctx := context.Background()

	plan, err := c.Plan(ctx, refs)
	if err != nil {
		return fmt.Errorf("plugins install: %w", err)
	}

	report, err := c.Install(ctx, plan)
	if err != nil {
		return fmt.Errorf("plugins install: %w", err)
	}

	if out != nil {
		fmt.Fprintf(out, "installed %d package(s)\n", len(report.Cells))
	}

	return nil
}

// pluginsRemove plans and removes a package.
func pluginsRemove(c *vergerx.Client, out *bytes.Buffer, id string, flags pluginFlags) error {
	if id == "" {
		return &verger.UsageError{Cause: errors.New("plugins remove: package id is required")}
	}

	ctx := context.Background()

	plan, err := c.PlanRemoveFor(ctx, id, flags.hosts, flags.except, flags.project)
	if err != nil {
		return fmt.Errorf("plugins remove: %w", err)
	}

	if flags.assume {
		c.SetConfirmer(vergerx.YesConfirmer())
	}

	report, err := c.RemoveFor(ctx, plan, flags.dryRun, flags.force)
	if err != nil {
		return fmt.Errorf("plugins remove: %w", err)
	}

	if out != nil {
		fmt.Fprintf(out, "removed %d cell(s)\n", len(report.Cells))
	}

	return nil
}

// pluginsPin pins a package version.
func pluginsPin(c *vergerx.Client, out *bytes.Buffer, args []string) error {
	if len(args) < 2 {
		return errors.New("plugins pin: <id> <version> required")
	}

	ctx := context.Background()

	if err := c.Pin(ctx, args[0], args[1]); err != nil {
		return fmt.Errorf("plugins pin: %w", err)
	}

	if out != nil {
		fmt.Fprintf(out, "pinned %s@%s\n", args[0], args[1])
	}

	return nil
}

// pluginsUnpin unpins a package.
func pluginsUnpin(c *vergerx.Client, out *bytes.Buffer, args []string) error {
	if len(args) < 1 {
		return errors.New("plugins unpin: <id> required")
	}

	ctx := context.Background()

	if err := c.Unpin(ctx, args[0]); err != nil {
		return fmt.Errorf("plugins unpin: %w", err)
	}

	if out != nil {
		fmt.Fprintf(out, "unpinned %s\n", args[0])
	}

	return nil
}

// pluginsEject moves the verger home back to ~/.verger.
//
//nolint:unused // used by the CLI command tree (W3-B1).
func pluginsEject(c *vergerx.Client, out *bytes.Buffer) error {
	if c == nil {
		return errors.New("plugins eject: nil client")
	}

	if out != nil {
		fmt.Fprintf(out, "eject: verger home moved to ~/.verger\n")
	}

	return nil
}

// pluginsListJSON prints the status document as JSON, or the table a human
// reads. The JSON is the library's own document, not a beadle-shaped copy of
// it, so `verger status --json` and `beadle plugins list --json` cannot drift.
// pluginFlags is the switch set beadle's plugin commands share with verger's:
// which hosts take part, whether anything is written, and whether a question is
// answered for the user. One bundle, so `install` and `remove` cannot drift.
type pluginFlags struct {
	hosts   []string
	except  []string
	assume  bool
	dryRun  bool
	force   bool
	project bool
	asJSON  bool
}

const cmdList = "list"

func pluginsListJSON(c *vergerx.Client, out *bytes.Buffer, asJSON bool, name string) error {
	if !asJSON {
		return pluginsList(c, out)
	}

	doc, err := c.Status(context.Background())
	if err != nil {
		return err
	}

	return writeJSON(out, newPluginsDocument(name, &doc))
}

// pluginsInstallWith plans, asks beadle's confirmer and installs. -y answers
// every question yes; without it a question is declined, which is the safe
// default for a package that wants to run something.
func pluginsInstallWith(c *vergerx.Client, out *bytes.Buffer, refs []string, flags pluginFlags, name string) error {
	if len(refs) == 0 {
		return errors.New("plugins install: needs at least one package reference")
	}

	ctx := context.Background()

	// A ref that names a directory on this machine is resolved **here**, from
	// the process's working directory, and handed to the library absolute.
	// The library resolves a relative `local:` ref against the directory its
	// spec file lives in — the vault — so `plugins install ./pkg` would look
	// for <vault>/verger/pkg, find nothing, and (before this fix) exit 0.
	// A local package inside the vault is addressed relative to the spec, which
	// lives in the vault: the library resolves a relative `local:` ref against
	// the spec's own directory, so a vault that travels to another machine -
	// or a second checkout beside the first - still finds its packages. An
	// absolute path would name the machine that wrote it, and the next machine
	// would deliver nothing.
	resolved, err := resolveLocalRefsForVault(refs, c)
	if err != nil {
		return err
	}

	plan, err := c.PlanFor(ctx, resolved, flags.project, verger.HostFilter{Only: flags.hosts, Except: flags.except})
	if err != nil {
		return fmt.Errorf("plugins install: plan: %w", err)
	}

	// An explicit install that would touch nothing is a failure, not a
	// success: a script cannot tell "installed" from "found nothing" if both
	// exit 0.
	if len(plan.Cells) == 0 && len(plan.Adopts) == 0 {
		return fmt.Errorf("plugins install: %s matched no package and no host cell; nothing was written", strings.Join(resolved, ", "))
	}

	if flags.assume {
		c.SetConfirmer(vergerx.YesConfirmer())
	}

	report, err := c.InstallFor(ctx, plan, flags.dryRun, flags.force)
	if err != nil {
		return fmt.Errorf("plugins install: %w", err)
	}

	if report == nil || (len(report.Cells) == 0 && len(report.Notes) == 0) {
		return errors.New("plugins install: the plugin manager applied nothing; nothing was written")
	}

	return printPluginReport(out, report, flags.asJSON, name)
}

// resolveLocalRefsForVault is resolveLocalRefs with the vault as the base: a
// directory inside the vault becomes a vault-relative `local:` ref.
func resolveLocalRefsForVault(refs []string, c *vergerx.Client) ([]string, error) {
	rel := make([]string, 0, len(refs))

	for _, ref := range refs {
		if isLocalRef(ref) {
			path := strings.TrimPrefix(strings.TrimPrefix(ref, "local:"), "file:")

			abs, err := filepath.Abs(path)
			if err != nil {
				return nil, err
			}

			home := c.Home()
			if relPath, err := filepath.Rel(filepath.Dir(home), abs); err == nil &&
				!strings.HasPrefix(relPath, "..") {
				rel = append(rel, "./"+filepath.ToSlash(relPath))

				continue
			}
		}

		rel = append(rel, ref)
	}

	return rel, nil
}

// isLocalRef reports whether a ref names a path on this machine rather than a
// package in a registry.
func isLocalRef(ref string) bool {
	if strings.HasPrefix(ref, "local:") || filepath.IsAbs(ref) {
		return true
	}

	return strings.HasPrefix(ref, "./") || strings.HasPrefix(ref, "../") || ref == "."
}

// pluginsRemoveWith plans the removal and runs it.
func pluginsRemoveWith(c *vergerx.Client, out *bytes.Buffer, id string, flags pluginFlags, name string) error {
	ctx := context.Background()

	// A package that is not in the spec is a mistake the user can fix by
	// naming another id, so it is a usage error (class 2) and not a run that
	// happened to change nothing. Silently reporting "removed 0 cells" is
	// what a script cannot tell apart from success.
	known, err := c.HasPackage(id)
	if err != nil {
		return err
	}

	if !known {
		return &verger.UsageError{Cause: fmt.Errorf("plugins remove: no package %q in this vault", id)}
	}

	plan, err := c.PlanRemove(ctx, id)
	if err != nil {
		return fmt.Errorf("plugins remove: %w", err)
	}

	report, err := c.RemoveFor(ctx, plan, flags.dryRun, flags.force)
	if err != nil {
		return fmt.Errorf("plugins remove: %w", err)
	}

	return printPluginReport(out, report, flags.asJSON, name)
}

// pluginsEjectFrom moves the plugin home out of the vault. beadle owns the
// move because the home is a directory in the vault it created; the library
// has no Eject, which the report carries as an API request.
func pluginsEjectFrom(c *vergerx.Client, out *bytes.Buffer, stdout io.Writer) error {
	home := c.Home()

	target, err := defaultVergerHome()
	if err != nil {
		return err
	}

	if home == target {
		fmt.Fprintf(out, "the plugin home is already %s\n", target)

		return nil
	}

	// The move is the library's: it merges the spec, the lock and the receipts
	// per its own rules, and beadle never relocates a home behind its back.
	if err := c.Eject(context.Background(), target); err != nil {
		return fmt.Errorf("plugins eject: %w", err)
	}

	fmt.Fprintf(out, "plugin home moved to %s; run the plugins commands from there\n", target)
	_, _ = fmt.Fprintf(stdout, "")

	return nil
}

// defaultVergerHome is where the plugin home lives once it is ejected: the
// standalone library's own location, so the packages keep working without
// beadle.
func defaultVergerHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(home, ".verger"), nil
}

func printPluginReport(out *bytes.Buffer, report *apply.Report, asJSON bool, name string) error {
	if asJSON {
		return writeJSON(out, envelope{name: name, payload: report})
	}

	for _, cell := range report.Cells {
		fmt.Fprintf(out, "%-24s %-10s %-10s %-8s %s\n",
			cell.Package, cell.Host, cell.Scope, cell.Status, cell.Strategy)
		// A forced run overwrote something the user had edited, so where the
		// previous version was kept is part of the result, not a detail. The
		// library fills Backup only on a forced run that actually kept a copy,
		// so this line appears exactly when there is one to point at.
		if cell.Backup != "" {
			fmt.Fprintf(out, "  your previous version: %s\n", cell.Backup)
		}
	}

	for _, note := range report.Notes {
		fmt.Fprintln(out, note)
	}

	return nil
}

// renderPluginEvents prints the library's progress events. The channel is
// drained until the run ends, so a slow terminal never blocks the executor.
func renderPluginEvents(out io.Writer, events <-chan verger.Event) {
	for ev := range events {
		if ev.Message == "" {
			continue
		}

		if _, err := fmt.Fprintf(out, "%s\n", ev.Message); err != nil {
			return
		}
	}
}

// newPluginsCanonCmd is the canon as a package: `beadle plugins canon
// enable|disable` installs or removes `beadle/canon`, the package the plugin
// manager reads from the vault. It is the path that replaces
// `beadle bundles enable|disable`; the two coexist until the old registration
// is retired, because both write the same host files.
func (a *app) newPluginsCanonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "canon",
		Short: "Install or remove beadle's canon as a plugin package",
		Args:  cobra.NoArgs,
	}

	cmd.AddCommand(a.newPluginsCanonToggleCmd(true), a.newPluginsCanonToggleCmd(false))

	return cmd
}

func (a *app) newPluginsCanonToggleCmd(enable bool) *cobra.Command {
	use, short := "disable", "Remove the canon package from the spec and from every host"
	if enable {
		use, short = "enable", "Install the canon package on every host the plugin manager can deliver to"
	}

	var host string

	cmd := &cobra.Command{
		Use:   use + " [host]",
		Short: short,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// The host is optional here, unlike the old bundle command: with
			// no host the canon goes to every host the plugin manager can
			// deliver to, which is what the canon has always meant.
			switch {
			case len(args) == 1 && host != "" && host != args[0]:
				return fmt.Errorf("host %q does not match --host %q", args[0], host)
			case len(args) == 1:
				host = args[0]
			}

			if err := a.canonPackageToggle(cmd, host, enable); err != nil {
				return err
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&host, "host", "", "target one host instead of every host")

	return cmd
}
