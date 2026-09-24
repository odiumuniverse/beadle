package daemon

import (
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const launchdSubdir = "Library/LaunchAgents"

func RenderLaunchd(spec Spec) (string, string, error) {
	args, err := argsFor(spec)
	if err != nil {
		return "", "", err
	}

	label := labelOrDefault(spec)
	path := filepath.Join(spec.Home, launchdSubdir, label+".plist")

	fileLimit := spec.FileLimit
	if fileLimit <= 0 {
		fileLimit = 8192
	}

	var b strings.Builder

	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	b.WriteString("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	writePlistString(&b, "Label", label)
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")

	for _, arg := range args {
		b.WriteString("\t\t<string>" + xmlEscape(arg) + "</string>\n")
	}

	b.WriteString("\t</array>\n")
	b.WriteString("\t<key>RunAtLoad</key><true/>\n")
	b.WriteString("\t<key>KeepAlive</key><true/>\n")
	b.WriteString("\t<key>ThrottleInterval</key><integer>10</integer>\n")
	b.WriteString("\t<key>ProcessType</key><string>Background</string>\n")
	b.WriteString("\t<key>SoftResourceLimits</key><dict><key>NumberOfFiles</key><integer>" + strconv.Itoa(fileLimit) + "</integer></dict>\n")

	if len(spec.Env) > 0 {
		b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")

		for _, pair := range EnvPairs(spec.Env) {
			b.WriteString("\t\t<key>" + xmlEscape(pair[0]) + "</key><string>" + xmlEscape(pair[1]) + "</string>\n")
		}

		b.WriteString("\t</dict>\n")
	}

	if spec.LogPath != "" {
		writePlistString(&b, "StandardOutPath", spec.LogPath)
	}

	if spec.ErrLogPath != "" {
		writePlistString(&b, "StandardErrorPath", spec.ErrLogPath)
	}

	b.WriteString("</dict>\n</plist>\n")

	return path, b.String(), nil
}

func registerLaunchd(ctx context.Context, spec Spec, run Runner) error {
	path, _, err := RenderLaunchd(spec)
	if err != nil {
		return err
	}

	target := fmt.Sprintf("gui/%d", os.Getuid())

	if err := run(ctx, "launchctl", "bootstrap", target, path); err != nil {
		_ = run(ctx, "launchctl", "bootout", target+"/"+labelOrDefault(spec))

		if err := run(ctx, "launchctl", "bootstrap", target, path); err != nil {
			return fmt.Errorf("launchctl bootstrap: %w", err)
		}
	}

	return nil
}

func unregisterLaunchd(ctx context.Context, spec Spec, run Runner) error {
	_ = run(ctx, "launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), labelOrDefault(spec)))

	return nil
}

func writePlistString(b *strings.Builder, key, value string) {
	b.WriteString("\t<key>" + key + "</key><string>" + xmlEscape(value) + "</string>\n")
}

func xmlEscape(s string) string {
	var b strings.Builder

	_ = xml.EscapeText(&b, []byte(s))

	return b.String()
}
