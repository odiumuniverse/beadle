package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/odiumuniverse/beadle/pkg/agentplugins"
	"github.com/odiumuniverse/beadle/pkg/bundle"
	"github.com/odiumuniverse/beadle/pkg/hooks"
	"github.com/odiumuniverse/beadle/pkg/skill"
)

// canonPackage renders the canon as one portable Agent Plugins package at
// <vault>/bundle and writes it there. It uses the same renderer
// `beadle export agent-plugins` uses, so the package a user exports by hand and
// the package verger installs are the same bytes — only the destination
// differs.
// renderCanonPackage renders the canon package on a full forward sync. The
// canon is the same content the per-host bundles render, packaged once for the
// plugin manager to deliver: rendered from the canon, never from a host, so it
// cannot deliver itself back.
func (e *Engine) renderCanonPackage(ctx context.Context, report *Report, opts SyncOptions) {
	if opts.DryRun || !fullForwardSync(opts) {
		return
	}

	e.canonPackage(ctx, report)
}

func (e *Engine) canonPackage(ctx context.Context, report *Report) {
	skills, err := skill.ReadDir(e.vault.SkillsDir())
	if err != nil {
		e.log.Print(ctx, "canon package: not rendered", "reason", err.Error())

		return
	}

	servers, err := bundle.CanonServers(e.readServersBytes())
	if err != nil {
		e.log.Print(ctx, "canon package: not rendered", "reason", err.Error())

		return
	}

	// The same approval table the per-host bundles use: one rule, one source
	// of truth about what a hook's approval means.
	hookSet, approved := e.canonHooks()

	pkg, err := bundle.RenderCanonPackage(bundle.CanonPackageInput{
		Skills:   skills,
		Servers:  servers,
		Hooks:    hookSet,
		Approved: approved,
	})
	if err != nil {
		e.log.Print(ctx, "canon package: not rendered", "reason", err.Error())

		return
	}

	// What the package skipped is a property of the package, not of the
	// vault: `beadle export agent-plugins` prints it, and the sync log
	// records it. A sync that said nothing else would drown in a line per
	// skipped server.
	for _, warning := range pkg.Warnings {
		e.log.Print(ctx, "canon package: "+warning)
	}

	dir := e.vault.CanonPackageDir()

	// The canon package is the one tree beadle must never let a secret value
	// into: it is built to be read by another machine. A value that reached
	// here would travel with the vault, so the check is here, not in a doc.
	if err := e.assertNoSecretValues(dir, pkg.Files); err != nil {
		report.Warnings = append(report.Warnings, err.Error())

		return
	}

	writeReport, err := agentplugins.Write(dir, pkg.Files)
	if err != nil {
		e.log.Print(ctx, "canon package: not written", "reason", err.Error())

		return
	}

	for _, path := range writeReport.Pruned {
		e.log.Print(ctx, "canon package: removed stale", "path", path)
	}

	e.log.Print(ctx, "canon package: rendered", "files", len(pkg.Files), "version", pkg.Version)
}

// canonHooks is the canon's hook map and the approval keys that go with it.
// The package renderer filters on the approval table itself, so the rule lives
// in one place.
func (e *Engine) canonHooks() (map[string]hooks.Hook, map[string]bool) {
	canon, err := hooks.Load(e.vault.HooksPath())
	if err != nil {
		// A malformed hooks document is the bundle path's problem to report;
		// here it simply means no hook travels.
		return map[string]hooks.Hook{}, e.approvedHooksForRender(nil)
	}

	return canon, e.approvedHooksForRender(canon)
}

func (e *Engine) readServersBytes() []byte {
	data, _, err := readOptional(e.vault.ServersPath())
	if err != nil {
		return nil
	}

	return data
}

// assertNoSecretValues is the belt to the renderer's braces: if any file the
// package is about to write contains a value from beadle's secret store, the
// package is not written at all.
func (e *Engine) assertNoSecretValues(dir string, files agentplugins.Files) error {
	values := e.secretValues()

	for rel, data := range files {
		for _, value := range values {
			if value == "" {
				continue
			}

			if containsSecretValue(data, value) {
				return fmt.Errorf("canon package: %s carries a secret value; the package was not written", rel)
			}
		}
	}

	_ = dir

	return nil
}

func containsSecretValue(data []byte, value string) bool {
	return len(value) > 0 && bytes.Contains(data, []byte(value))
}

// secretValues is every value beadle's store holds. A package that carries any
// of them would ship a machine's secrets to another machine, so the check is
// made against the store itself rather than against a pattern.
func (e *Engine) secretValues() []string {
	if e.secrets == nil {
		return nil
	}

	names := e.secrets.Names()
	out := make([]string, 0, len(names))

	for _, name := range names {
		if value, ok := e.secrets.Get(name); ok {
			out = append(out, value)
		}
	}

	return out
}

// RenderCanonPackage renders the canon package on demand, so a command that
// installs it does not have to run a whole sync first. It reports nothing: the
// caller has its own output.
func (e *Engine) RenderCanonPackage(ctx context.Context) error {
	report := &Report{}
	e.canonPackage(ctx, report)

	joined := strings.Join(report.Warnings, "\n")
	if joined == "" {
		return nil
	}

	return errors.New(joined)
}
