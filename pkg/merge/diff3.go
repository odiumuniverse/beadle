package merge

import (
	"bytes"
	"cmp"
	"slices"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

const defaultVaultLabel = "vault"

type TextOptions struct {
	VaultLabel string
	AgentLabel string
}

type TextResult struct {
	Merged    []byte
	Conflicts int
}

func Text(base, vault, agent []byte, opts TextOptions) TextResult {
	vaultLabel := cmp.Or(opts.VaultLabel, defaultVaultLabel)
	agentLabel := cmp.Or(opts.AgentLabel, "agent")

	baseLines := splitLines(base)
	vaultLines := splitLines(vault)
	agentLines := splitLines(agent)

	vaultEq := equalRuns(baseLines, vaultLines)
	agentEq := equalRuns(baseLines, agentLines)

	var (
		out        bytes.Buffer
		conflicts  int
		bi, vi, ai int
	)

	for bi < len(baseLines) || vi < len(vaultLines) || ai < len(agentLines) {
		if bi < len(baseLines) {
			vOffset, vEqual := vaultEq[bi]
			aOffset, aEqual := agentEq[bi]

			if vEqual && aEqual && vOffset == vi && aOffset == ai {
				out.WriteString(baseLines[bi])
				bi++
				vi++
				ai++

				continue
			}
		}

		next, vaultEnd, agentEnd := nextRegion(baseLines, vaultEq, agentEq, bi, len(vaultLines), len(agentLines))
		baseSeg := baseLines[bi:next]
		vaultSeg := vaultLines[vi:vaultEnd]
		agentSeg := agentLines[ai:agentEnd]

		switch {
		case slices.Equal(vaultSeg, agentSeg):
			writeRaw(&out, vaultSeg)
		case slices.Equal(vaultSeg, baseSeg):
			writeRaw(&out, agentSeg)
		case slices.Equal(agentSeg, baseSeg):
			writeRaw(&out, vaultSeg)
		default:
			conflicts++

			writeConflict(&out, baseSeg, vaultSeg, agentSeg, vaultLabel, agentLabel)
		}

		bi, vi, ai = next, vaultEnd, agentEnd
	}

	return TextResult{Merged: out.Bytes(), Conflicts: conflicts}
}

func equalRuns(base, other []string) map[int]int {
	eq := make(map[int]int)
	if len(base) == 0 || len(other) == 0 {
		return eq
	}

	matcher := difflib.NewMatcher(base, other)

	for _, op := range matcher.GetOpCodes() {
		if op.Tag != 'e' {
			continue
		}

		for k := range op.I2 - op.I1 {
			eq[op.I1+k] = op.J1 + k
		}
	}

	return eq
}

func nextRegion(base []string, vaultEq, agentEq map[int]int, from, vaultLen, agentLen int) (int, int, int) {
	if from >= len(base) {
		return from, vaultLen, agentLen
	}

	next := from + 1
	for next < len(base) {
		_, vEqual := vaultEq[next]
		_, aEqual := agentEq[next]

		if vEqual && aEqual {
			break
		}

		next++
	}

	return next, mappedEnd(vaultEq, next, len(base), vaultLen), mappedEnd(agentEq, next, len(base), agentLen)
}

func mappedEnd(eq map[int]int, index, baseLen, sideLen int) int {
	if index >= baseLen {
		return sideLen
	}

	end, ok := eq[index]
	if !ok {
		return sideLen
	}

	return end
}

func writeRaw(out *bytes.Buffer, lines []string) {
	for _, line := range lines {
		out.WriteString(line)
	}
}

func writeConflict(out *bytes.Buffer, base, vault, agent []string, vaultLabel, agentLabel string) {
	out.WriteString("<<<<<<< " + vaultLabel + "\n")
	writeSection(out, vault)
	out.WriteString("||||||| base\n")
	writeSection(out, base)
	out.WriteString("=======\n")
	writeSection(out, agent)
	out.WriteString(">>>>>>> " + agentLabel + "\n")
}

func writeSection(out *bytes.Buffer, lines []string) {
	writeRaw(out, lines)

	if last := lastLine(lines); last != "" && !strings.HasSuffix(last, "\n") {
		out.WriteString("\n")
	}
}

func lastLine(lines []string) string {
	if len(lines) == 0 {
		return ""
	}

	return lines[len(lines)-1]
}

func splitLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}

	var lines []string

	for len(b) > 0 {
		idx := bytes.IndexByte(b, '\n')
		if idx < 0 {
			lines = append(lines, string(b))

			break
		}

		lines = append(lines, string(b[:idx+1]))
		b = b[idx+1:]
	}

	return lines
}
