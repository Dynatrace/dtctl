package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/diagnostic"
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/document"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
	"github.com/dynatrace-oss/dtctl/pkg/stability"
	"github.com/dynatrace-oss/dtctl/sdk/httpclient"
	"github.com/dynatrace-oss/dtctl/sdk/urls"
)

// claimCmd represents the claim command
var claimCmd = newClaimCmd()

func newClaimCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "claim",
		Short: "Claim access to a shared resource",
		Long:  `Claim access to a resource that was shared with you.`,
	}
	stability.Mark(c, stability.Experimental, "0.41.0")
	return c
}

// claimEnvironmentShareCmd claims an environment share, mirroring the
// browser's "Copy link to share" -> open flow.
var claimEnvironmentShareCmd = newClaimEnvironmentShareCmd()

func newClaimEnvironmentShareCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "environment-share <share-id | share-url>",
		Aliases: []string{"share", "envshare"},
		Short:   "Claim an environment share by ID or URL",
		Long: `Claim an environment share and learn which document it points to.

A share link copied from the browser carries only the share ID:

  https://<environment>.apps.dynatrace.com/ui/document/v0/#share=<share-id>

Claiming is the resolution step: only the share's owner can look a share up,
so a recipient claims it and gets back the document's ID, name, type, the
access now held and its URL (-o wide). Accepts the bare ID or the pasted URL.

Claiming cannot be undone by the claimer (only the owner can delete the
share). Claiming your own share fails; claiming twice changes nothing.

Examples:
  dtctl claim environment-share 018f1234-abcd-7000-8000-000000000000
  dtctl claim environment-share 'https://xyz.apps.dynatrace.com/ui/document/v0/#share=018f1234-abcd-7000-8000-000000000000'
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			shareID, host, err := document.ParseShareRef(args[0])
			if err != nil {
				return err
			}

			cfg, c, printer, err := setupWithSafetyAndPrinter(cmdContext(cmd), safety.OperationCreate)
			if err != nil {
				return err
			}

			if host != "" {
				if ctx, cerr := cfg.CurrentContextObj(); cerr == nil && !sameEnvironment(host, ctx.Environment) {
					return shareHostMismatch(cfg, host, ctx.Environment)
				}
			}

			if dryRun(cmdContext(cmd)) {
				return newDryRunReport(cmd).
					Linef("Dry run: would claim environment share %q", shareID).
					Detail("share_id", "%s", shareID).
					Print()
			}

			h := document.NewHandler(c)
			claim, err := h.ClaimEnvironmentShare(shareID)
			if err != nil {
				return claimError(err, cfg, shareID)
			}

			var warnings []string
			if meta, merr := h.GetMetadata(claim.DocumentID); merr == nil {
				claim.Name = meta.Name
			} else {
				warnings = append(warnings, fmt.Sprintf("claimed, but reading the document's name failed: %v", merr))
			}
			claim.URL = document.UIURL(c.BaseURL(), claim.DocumentType, claim.DocumentID)

			if ap := enrichAgent(printer, "claim", "environment-share"); ap != nil {
				ap.SetSuggestions([]string{claimFollowUp(claim)})
				ap.SetWarnings(warnings)
			} else {
				for _, w := range warnings {
					output.FprintWarning(currentStderr(cmdContext(cmd)), "%s", w)
				}
			}
			return printer.Print(claim)
		},
	}
	stability.Mark(c, stability.Experimental, "0.41.0")
	return c
}

// sameEnvironment reports whether a share link's host is the environment's.
func sameEnvironment(linkHost, environmentURL string) bool {
	envHost := urls.Host(environmentURL)
	return envHost == "" || linkHost == envHost
}

// shareHostMismatch names the configured context that targets the link's
// environment, or how to add one.
func shareHostMismatch(cfg *config.Config, linkHost, environmentURL string) error {
	var suggestions []string
	for _, nc := range cfg.Contexts {
		if nc.Name != cfg.CurrentContext && urls.Host(nc.Context.Environment) == linkHost {
			suggestions = append(suggestions, fmt.Sprintf("Re-run with --context %s", nc.Name))
		}
	}
	if len(suggestions) == 0 {
		suggestions = append(suggestions, fmt.Sprintf("Add a context for it: dtctl auth login --context <name> --environment https://%s", linkHost))
	}
	return &diagnostic.Error{
		Operation:   "claim environment-share",
		Message:     fmt.Sprintf("share link is for %s, but the current context targets %s", linkHost, urls.Host(environmentURL)),
		Suggestions: suggestions,
	}
}

// claimError replaces the claim endpoint's bare 400/403/404 with what each
// means for a share link.
func claimError(err error, cfg *config.Config, shareID string) error {
	var apiErr *httpclient.APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	d := &diagnostic.Error{Operation: "claim environment-share", StatusCode: apiErr.StatusCode, Err: err}
	switch apiErr.StatusCode {
	case 400:
		d.Message = "you own this share, and owners cannot claim their own shares (" + apiErrText(apiErr) + ")"
		d.Suggestions = []string{"You already have access to the document: find it with 'dtctl get documents --mine'"}
	case 403:
		d.Message = "access denied: " + apiErrText(apiErr)
		d.Suggestions = append(loginScopeAdvice(cfg, []string{claimScope}),
			fmt.Sprintf("Platform token: create it with the %s scope", claimScope),
			"Check the scope gate: re-run the same command with --check-scopes")
	case 404:
		d.Message = fmt.Sprintf("environment share %q not found", shareID)
		d.Suggestions = []string{
			"The owner may have deleted the share; ask them for a new link",
			"Check that the current context targets the environment the link came from",
		}
	default:
		return err
	}
	return d
}

const claimScope = "document:environment-shares:claim"

// apiErrText keeps the response body, which carries the errorRef support asks for.
func apiErrText(e *httpclient.APIError) string {
	if e.Details != "" {
		return e.Message + " - " + e.Details
	}
	return e.Message
}

// claimFollowUp is the agent-mode suggestion for reading the claimed document.
func claimFollowUp(c *document.EnvironmentShareClaim) string {
	switch c.DocumentType {
	case "dashboard", "notebook":
		return fmt.Sprintf("dtctl get %s %s", c.DocumentType, c.DocumentID)
	}
	return fmt.Sprintf("dtctl get document %s", c.DocumentID)
}

func init() {
	rootCmd.AddCommand(claimCmd)
	claimCmd.AddCommand(claimEnvironmentShareCmd)
}
