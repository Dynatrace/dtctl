package cmd

import (
	"errors"
	"fmt"
	"strings"

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
var claimCmd = &cobra.Command{
	Use:   "claim",
	Short: "Claim access to a shared resource",
	Long:  `Claim access to a resource that was shared with you.`,
}

// claimEnvironmentShareCmd claims an environment share, mirroring the
// browser's "Copy link to share" -> open flow.
var claimEnvironmentShareCmd = &cobra.Command{
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
share). Claiming twice changes nothing. Claiming your own share is rejected by
the API, so dtctl looks the share up as its owner instead and prints the
document it points to without claiming anything (this needs the
document:environment-shares:read scope, i.e. a readwrite-all context).

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

		cfg, c, printer, err := SetupWithSafetyAndPrinter(safety.OperationCreate)
		if err != nil {
			return err
		}

		if host != "" {
			if ctx, cerr := cfg.CurrentContextObj(); cerr == nil && !sameEnvironment(host, ctx.Environment) {
				return shareHostMismatch(cfg, host, ctx.Environment)
			}
		}

		if dryRun {
			return newDryRunReport(cmd).
				Linef("Dry run: would claim environment share %q", shareID).
				Detail("share_id", "%s", shareID).
				Print()
		}

		h := document.NewHandler(c)
		var warnings []string
		claim, err := h.ClaimEnvironmentShare(shareID)
		if err != nil {
			// A 400 means the caller owns the share. Only the owner can look a
			// share up, so resolve it that way instead of failing.
			if isOwnShareError(err) {
				var note string
				if claim, note = resolveOwnShare(h, shareID); claim != nil {
					warnings = append(warnings, note)
				}
			}
			if claim == nil {
				return claimError(err, cfg, shareID)
			}
		}

		if claim.Name == "" {
			if meta, merr := h.GetMetadata(claim.DocumentID); merr == nil {
				claim.Name = meta.Name
			} else {
				warnings = append(warnings, fmt.Sprintf("claimed, but reading the document's name failed: %v", merr))
			}
		}
		claim.URL = document.UIURL(c.BaseURL(), claim.DocumentType, claim.DocumentID)

		if ap := enrichAgent(printer, "claim", "environment-share"); ap != nil {
			ap.SetSuggestions([]string{claimFollowUp(claim)})
			ap.SetWarnings(warnings)
		} else {
			for _, w := range warnings {
				output.PrintWarning("%s", w)
			}
		}
		return printer.Print(claim)
	},
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

// isOwnShareError reports whether the claim endpoint rejected the call because
// the caller owns the share.
func isOwnShareError(err error) bool {
	var apiErr *httpclient.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == 400
}

// resolveOwnShare looks up a share the caller owns and describes the document
// it points to, with the access the caller holds on it. It returns nil when the
// lookup is not possible (no environment-shares:read scope, share gone), so the
// caller can fall back to the own-share error.
func resolveOwnShare(h *document.Handler, shareID string) (*document.EnvironmentShareClaim, string) {
	share, err := h.GetEnvironmentShare(shareID)
	if err != nil {
		return nil, ""
	}
	claim := &document.EnvironmentShareClaim{DocumentID: share.DocumentID}
	note := fmt.Sprintf("you own this share, so nothing was claimed; it gives recipients %s access", strings.Join(share.Access, ","))
	if meta, merr := h.GetMetadata(share.DocumentID); merr == nil {
		claim.Name, claim.DocumentType = meta.Name, meta.Type
		claim.Access, claim.AccessLevel = meta.Access, strings.Join(meta.Access, ",")
	}
	return claim, note
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

	stability.Mark(claimCmd, stability.Experimental, "0.41.0")
	stability.Mark(claimEnvironmentShareCmd, stability.Experimental, "0.41.0")
}
