package cmd

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/platformtoken"
	"github.com/dynatrace-oss/dtctl/pkg/safety"
)

var accountListCmd = newAccountListCmd()

func newAccountListCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "list",
		Short: "List account resources",
		RunE:  requireSubcommand,
	}
	return c
}

var accountListTokenCmd = newAccountListTokenCmd()

func newAccountListTokenCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "token",
		Short: "List platform tokens",
		Long: `List all platform tokens for the account.

Examples:
  # List all tokens
  dtctl account list token

  # Output as JSON
  dtctl account list token -o json
`,
		Aliases: []string{"tokens"},
		RunE: func(cmd *cobra.Command, args []string) error {
			accClient, accountUUID, err := setupAccount(cmdContext(cmd))
			if err != nil {
				return err
			}

			handler := platformtoken.NewHandler(accClient, accountUUID)
			tokens, err := handler.List()
			if err != nil {
				return err
			}

			return newPrinterCtx(cmdContext(cmd)).PrintList(tokens)
		},
	}
	return c
}

var accountCreateCmd = newAccountCreateCmd()

func newAccountCreateCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "create",
		Short: "Create account resources",
		RunE:  requireSubcommand,
	}
	return c
}

var accountCreateTokenCmd = newAccountCreateTokenCmd()

func newAccountCreateTokenCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "token",
		Short: "Create a platform token",
		Long: `Create a new Dynatrace platform token.

The --user-uuid flag is optional; if omitted, the current user's UUID is
resolved automatically from the account token's JWT subject claim.

Examples:
  # Create a token with 90-day expiry (default)
  dtctl account create token --name ci-pipeline --scope account-idm-read

  # Create a token with multiple scopes (repeat or comma-separate)
  dtctl account create token --name ci-pipeline --scope account-idm-read,storage:buckets:read
  dtctl account create token --name ci-pipeline --scope account-idm-read --scope storage:buckets:read

  # Create a token expiring in 30 days
  dtctl account create token --name ci-pipeline --scope account-idm-read --expires 30d

  # Create with explicit expiration date (RFC3339)
  dtctl account create token --name ci-pipeline --scope account-idm-read --expires-at 2026-10-01T00:00:00Z

  # Create for a specific user
  dtctl account create token --name ci-pipeline --scope account-idm-read --user-uuid <uuid>

  # Dry run to preview
  dtctl account create token --name ci-pipeline --scope account-idm-read --dry-run
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _ := cmd.Flags().GetString("name")
			rawScopes, _ := cmd.Flags().GetStringArray("scope")
			scopes := normalizeScopes(rawScopes)
			expires, _ := cmd.Flags().GetString("expires")
			expiresAt, _ := cmd.Flags().GetString("expires-at")
			userUUID, _ := cmd.Flags().GetString("user-uuid")
			resources, _ := cmd.Flags().GetStringArray("resource")
			tags, _ := cmd.Flags().GetStringArray("tag")

			// --scope is marked required and rejects a blank value at parse time;
			// a value made only of separators ("--scope ,") gets past both and
			// still names no scope.
			if len(scopes) == 0 {
				return emptyFlagValueError("scope")
			}
			if expiresAt != "" && cmd.Flags().Changed("expires") {
				return fmt.Errorf("--expires and --expires-at are mutually exclusive")
			}

			var expirationDate string
			if expiresAt != "" {
				t, err := time.Parse(time.RFC3339, expiresAt)
				if err != nil {
					return fmt.Errorf("invalid --expires-at value %q: must be RFC3339 (e.g. 2026-10-01T00:00:00Z): %w", expiresAt, err)
				}
				expirationDate = t.UTC().Format("2006-01-02T15:04:05.000Z")
			} else {
				t, err := parseExpiresDuration(expires)
				if err != nil {
					return err
				}
				expirationDate = t.UTC().Format("2006-01-02T15:04:05.000Z")
			}

			accClient, accountUUID, err := setupAccountWithSafety(cmdContext(cmd), safety.OperationCreate)
			if err != nil {
				return err
			}

			if userUUID == "" {
				userUUID, err = resolveCurrentAccountUserUUID(cmdContext(cmd), accountUUID)
				if err != nil {
					return fmt.Errorf("could not auto-resolve user UUID from account token: %w\nHint: provide --user-uuid explicitly", err)
				}
			}

			if len(resources) == 0 {
				resources = []string{"urn:dtaccount:" + accountUUID}
			}

			req := platformtoken.PlatformTokenCreate{
				Name:           name,
				UserUUID:       userUUID,
				Scope:          scopes,
				Resource:       resources,
				Tags:           tags,
				ExpirationDate: expirationDate,
			}

			if dryRun(cmdContext(cmd)) {
				report := newDryRunReport(cmd).As("create", "token").OnStderr().
					Linef("Dry run: would create platform token").
					Linef("Name:     %s", req.Name).
					Linef("Scope:    %s", strings.Join(req.Scope, ", ")).
					Linef("UserUUID: %s", req.UserUUID).
					Linef("Expires:  %s", req.ExpirationDate).
					Detail("name", "%s", req.Name).
					Detail("scope", "%s", strings.Join(req.Scope, ", ")).
					Detail("user_uuid", "%s", req.UserUUID).
					Detail("expires", "%s", req.ExpirationDate)
				// The request carries no secret — the token is minted by the server —
				// so the exact body is safe to show.
				if body, err := json.Marshal(req); err == nil {
					report.Payload(body)
				}
				return report.Print()
			}

			handler := platformtoken.NewHandler(accClient, accountUUID)
			res, err := handler.Create(req)
			if err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Platform token %q created (expires: %s)", res.Name, expirationDate)
			output.FprintWarning(currentStderr(cmdContext(cmd)), "Token secret shown once — store it now:")
			fmt.Fprintln(currentStdout(cmdContext(cmd)), res.Token)
			return nil
		},
	}
	c.Flags().String("name", "", "token name (required)")
	c.Flags().StringArray("scope", nil, "token scope; repeat or comma/space/newline-separate for multiple (required)")
	c.Flags().String("expires", "90d", "token lifetime (e.g. 30d, 720h)")
	c.Flags().String("expires-at", "", "exact expiration date in RFC3339 format (mutually exclusive with --expires)")
	c.Flags().String("user-uuid", "", "user UUID the token belongs to (default: current user)")
	c.Flags().StringArray("resource", nil, "environment URL(s) the token is scoped to (default: current environment)")
	c.Flags().StringArray("tag", nil, "token tag; may be specified multiple times")
	markFlagRequiredNonEmpty(c, "name")
	markFlagRequiredNonEmpty(c, "scope")
	return c
}

var accountDeleteCmd = newAccountDeleteCmd()

func newAccountDeleteCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "delete",
		Short: "Delete account resources",
		RunE:  requireSubcommand,
	}
	return c
}

var accountDeleteTokenCmd = newAccountDeleteTokenCmd()

func newAccountDeleteTokenCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "token <tokenId>",
		Aliases: []string{"revoke"},
		Short:   "Delete (revoke) a platform token",
		Long: `Delete (revoke) a platform token by its ID.

Examples:
  # Delete a token
  dtctl account delete token <tokenId>
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tokenID := args[0]

			if dryRun(cmdContext(cmd)) {
				return newDryRunReport(cmd).As("delete", "token").OnStderr().
					Linef("Dry run: would delete platform token %q", tokenID).
					Detail("id", "%s", tokenID).
					Print()
			}

			accClient, accountUUID, err := setupAccountWithSafety(cmdContext(cmd), safety.OperationDelete)
			if err != nil {
				return err
			}

			handler := platformtoken.NewHandler(accClient, accountUUID)
			if err := handler.Revoke(tokenID); err != nil {
				return err
			}

			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Platform token %q deleted", tokenID)
			return nil
		},
	}
	return c
}

// normalizeScopes splits and trims scope values so both "a, b" and "a b" work.
func normalizeScopes(scopes []string) []string {
	var result []string
	for _, s := range scopes {
		s = strings.ReplaceAll(s, ",", " ")
		result = append(result, strings.Fields(s)...)
	}
	return result
}

// parseExpiresDuration parses strings like "90d" or "720h" into a future time.
func parseExpiresDuration(s string) (time.Time, error) {
	if strings.HasSuffix(s, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid duration %q: expected Nd (e.g. 90d)", s)
		}
		return time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid duration %q: %w", s, err)
	}
	return time.Now().UTC().Add(d), nil
}

func init() {
	accountCmd.AddCommand(accountListCmd)
	accountListCmd.AddCommand(accountListTokenCmd)

	accountCmd.AddCommand(accountCreateCmd)
	accountCreateCmd.AddCommand(accountCreateTokenCmd)

	accountCmd.AddCommand(accountDeleteCmd)
	accountDeleteCmd.AddCommand(accountDeleteTokenCmd)

	// Create flags
}
