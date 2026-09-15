package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/odiumuniverse/agents-sync/pkg/engine"
)

func (a *app) newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose vault, sync state and agent configuration problems",
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := a.engine()
			if err != nil {
				return err
			}

			issues, err := e.Doctor(cmd.Context())
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()

			if len(issues) == 0 {
				fmt.Fprintln(out, "no issues found")

				return nil
			}

			errorCount := 0

			for _, issue := range issues {
				fmt.Fprintf(out, "[%-5s] %-26s %s\n", issue.Severity, issueScope(issue), issue.Message)

				if issue.Severity == engine.SeverityError {
					errorCount++
				}
			}

			if errorCount > 0 {
				return fmt.Errorf("doctor found %d error(s)", errorCount)
			}

			return nil
		},
	}
}

func issueScope(issue engine.Issue) string {
	switch {
	case issue.Agent != "" && issue.Kind != "":
		return issue.Agent + "/" + string(issue.Kind)
	case issue.Agent != "":
		return issue.Agent
	case issue.Kind != "":
		return string(issue.Kind)
	default:
		return "vault"
	}
}
