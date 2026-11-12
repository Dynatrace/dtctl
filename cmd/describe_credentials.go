package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dynatrace/dtctl/pkg/auth"
	"github.com/Dynatrace/dtctl/pkg/output"
	"github.com/Dynatrace/dtctl/pkg/resources/credentialvault"
	"github.com/Dynatrace/dtctl/pkg/stability"
)

// describeCredentialCmd shows the metadata of a credential vault entry.
var describeCredentialCmd = newDescribeCredentialCmd()

func newDescribeCredentialCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "credential <id>",
		Aliases: []string{"credentials", "cred"},
		Short:   "Show details of a credential vault entry",
		Long: `Show the metadata of a credential vault entry. Secrets are never returned.

Examples:
  dtctl describe credential CREDENTIALS_VAULT-0123456789ABCDEF
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}

			cred, err := credentialvault.NewHandler(c).Get(args[0])
			if err != nil {
				return credentialAccessDenied(err, "describe credential", auth.AccessRead)
			}

			if outputFormat(cmdContext(cmd)) == "table" {
				printCredentialDescribe(cmd, cred)
				return nil
			}

			enrichAgent(printer, "describe", "credential")
			return printer.Print(cred)
		},
	}
	stability.Mark(c, stability.Experimental, credentialsSince)
	return c
}

func printCredentialDescribe(cmd *cobra.Command, cred *credentialvault.Credential) {
	w := currentStdout(cmdContext(cmd))
	const width = 19
	output.FprintDescribeKV(w, "ID:", width, "%s", cred.ID)
	output.FprintDescribeKV(w, "Name:", width, "%s", cred.Name)
	output.FprintDescribeKV(w, "Type:", width, "%s", cred.Type)
	output.FprintDescribeKV(w, "Scopes:", width, "%s", strings.Join(cred.Scopes, ", "))
	if cred.Description != "" {
		output.FprintDescribeKV(w, "Description:", width, "%s", cred.Description)
	}
	if cred.Owner != "" {
		output.FprintDescribeKV(w, "Owner:", width, "%s", cred.Owner)
	}
	output.FprintDescribeKV(w, "Owner Access Only:", width, "%v", cred.OwnerAccessOnly)
	if cred.AllowContextlessRequests {
		output.FprintDescribeKV(w, "Contextless:", width, "%v", cred.AllowContextlessRequests)
	}

	if len(cred.AllowedEntities) > 0 {
		fmt.Fprintln(w)
		output.FprintDescribeSection(w, "Allowed Entities:")
		for _, e := range cred.AllowedEntities {
			fmt.Fprintf(w, "  - %s (%s)\n", e.ID, e.Type)
		}
	}
}
