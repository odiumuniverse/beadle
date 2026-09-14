package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	syncer "github.com/odiumuniverse/agents-sync/pkg/sync"
)

func (a *app) newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose vault and agent configuration problems",
		RunE: func(cmd *cobra.Command, _ []string) error {
			engine, err := a.engine()
			if err != nil {
				return err
			}

			issues, err := engine.Doctor(cmd.Context())
			if err != nil {
				return err
			}

			if len(issues) == 0 {
				cmd.Println("no issues found")

				return nil
			}

			errors := 0

			for _, issue := range issues {
				cmd.Printf("[%-5s] %-24s %s\n", issue.Severity, issueScope(issue), issue.Message)

				if issue.Severity == syncer.SeverityError {
					errors++
				}
			}

			if errors > 0 {
				return fmt.Errorf("doctor found %d error(s)", errors)
			}

			return nil
		},
	}
}

func issueScope(issue syncer.Issue) string {
	switch {
	case issue.Agent != "" && issue.Resource != "":
		return issue.Agent + "/" + issue.Resource
	case issue.Agent != "":
		return issue.Agent
	case issue.Resource != "":
		return issue.Resource
	default:
		return "general"
	}
}
