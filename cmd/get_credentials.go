package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dynatrace/dtctl/pkg/auth"
	"github.com/Dynatrace/dtctl/pkg/client"
	"github.com/Dynatrace/dtctl/pkg/output"
	"github.com/Dynatrace/dtctl/pkg/prompt"
	"github.com/Dynatrace/dtctl/pkg/resources/credentialvault"
	"github.com/Dynatrace/dtctl/pkg/safety"
	"github.com/Dynatrace/dtctl/pkg/stability"
	sdkcv "github.com/Dynatrace/dtctl/sdk/api/credentialvault"
)

// credentialsSince is the release the credential vault commands first ship in.
const credentialsSince = "0.46.0"

// getCredentialsCmd lists credential vault entries.
var getCredentialsCmd = newGetCredentialsCmd()

func newGetCredentialsCmd() *cobra.Command {
	var typ, scope, name string
	c := &cobra.Command{
		Use:     "credentials [id]",
		Aliases: []string{"credential", "cred"},
		Short:   "Get credential vault entries",
		Long: `Get credential vault entries (metadata only; secrets are never returned).

Examples:
  # List all credentials
  dtctl get credentials

  # List public certificates trusted for extension signing
  dtctl get credentials --type public-certificate --scope extension

  # Get a specific credential
  dtctl get credential CREDENTIALS_VAULT-0123456789ABCDEF -o yaml
`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				for _, f := range []string{"type", "scope", "name"} {
					if cmd.Flags().Changed(f) {
						return fmt.Errorf("--%s filters a list and cannot be combined with a credential ID", f)
					}
				}
			}
			opts := credentialvault.ListOptions{Name: name}
			if typ != "" {
				t, err := credentialvault.NormalizeType(typ)
				if err != nil {
					return err
				}
				opts.Type = t
			}
			if scope != "" {
				s, err := credentialvault.NormalizeScope(scope)
				if err != nil {
					return err
				}
				opts.Scope = s
			}

			_, c, printer, err := setup(cmdContext(cmd))
			if err != nil {
				return err
			}
			handler := credentialvault.NewHandler(c)

			if len(args) > 0 {
				cred, err := handler.Get(args[0])
				if err != nil {
					return credentialAccessDenied(err, "get credential", auth.AccessRead)
				}
				return printer.Print(cred)
			}

			list, err := handler.List(opts)
			if err != nil {
				return credentialAccessDenied(err, "list credentials", auth.AccessRead)
			}
			if hint := emptyCredentialListHint(list, opts); hint != "" {
				if ap := enrichAgent(printer, "get", "credentials"); ap != nil {
					ap.SetSuggestions([]string{hint})
				} else if outputFormat(cmdContext(cmd)) == "table" {
					fmt.Fprintln(currentStderr(cmdContext(cmd)), "Hint: "+hint)
				}
			}
			return printer.PrintList(list)
		},
	}
	c.Flags().StringVar(&typ, "type", "", "filter by type (public-certificate, certificate, token, username-password, ...)")
	c.Flags().StringVar(&scope, "scope", "", "filter by scope: extension (extension signing CAs), extension-authentication, synthetic, app-engine")
	c.Flags().StringVar(&name, "name", "", "filter by name (case-insensitive word search: any word may match; quote the value, e.g. '\"my ca\"', to match the whole phrase)")
	stability.Mark(c, stability.Experimental, credentialsSince)
	return c
}

// emptyCredentialListHint explains the one empty result that is a likely
// mistake: looking for an extension signing CA under extension-authentication,
// which holds the secrets an extension uses, not the CAs it is validated with.
func emptyCredentialListHint(list []credentialvault.Credential, opts credentialvault.ListOptions) string {
	if len(list) > 0 || opts.Scope != sdkcv.ScopeExtensionAuthentication {
		return ""
	}
	if opts.Type != "" && opts.Type != sdkcv.TypePublicCertificate {
		return ""
	}
	return "CAs that validate extension signatures use scope extension, not extension-authentication: try --scope extension"
}

// credentialOwnership compares a credential's owner with the current user. The
// vault reports the owner as an email address, so both the user ID and the
// email are checked; anything unresolvable is OwnershipUnknown.
func credentialOwnership(c *client.Client, owner string) safety.ResourceOwnership {
	if owner == "" {
		return safety.OwnershipUnknown
	}
	u, err := c.CurrentUser()
	if err != nil || u == nil {
		return safety.OwnershipUnknown
	}
	if owner == u.UserID || (u.EmailAddress != "" && strings.EqualFold(owner, u.EmailAddress)) {
		return safety.OwnershipOwn
	}
	if u.UserID == "" && u.EmailAddress == "" {
		return safety.OwnershipUnknown
	}
	return safety.OwnershipShared
}

// deleteCredentialCmd deletes a credential vault entry.
var deleteCredentialCmd = newDeleteCredentialCmd()

func newDeleteCredentialCmd() *cobra.Command {
	var forceDelete bool
	c := &cobra.Command{
		Use:     "credential <id>",
		Aliases: []string{"credentials", "cred"},
		Short:   "Delete a credential vault entry",
		Long: `Delete a credential vault entry by ID.

Examples:
  # Delete a credential
  dtctl delete credential CREDENTIALS_VAULT-0123456789ABCDEF

  # Delete without confirmation
  dtctl delete credential CREDENTIALS_VAULT-0123456789ABCDEF -y
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]

			cfg, c, err := setupClient(cmdContext(cmd))
			if err != nil {
				return err
			}
			handler := credentialvault.NewHandler(c)

			cred, err := handler.Get(id)
			if err != nil {
				return credentialAccessDenied(err, "get credential", auth.AccessRead)
			}
			if err := checkSafety(cmdContext(cmd), cfg, safety.OperationDelete, credentialOwnership(c, cred.Owner)); err != nil {
				return err
			}

			if dryRun(cmdContext(cmd)) {
				return deleteDryRun(cmd, "credential", cred.Name, id)
			}

			if !forceDelete && !plainMode(cmdContext(cmd)) {
				if !prompt.ConfirmDeletionWith(currentStdin(cmdContext(cmd)), currentStdout(cmdContext(cmd)), "credential", cred.Name, id) {
					fmt.Fprintln(currentStdout(cmdContext(cmd)), "Deletion cancelled")
					return nil
				}
			}

			if err := handler.Delete(id); err != nil {
				return credentialAccessDenied(err, "delete credential", auth.AccessDelete)
			}
			output.FprintSuccess(currentStderr(cmdContext(cmd)), "Credential %q deleted", cred.Name)
			return nil
		},
	}
	c.Flags().BoolVarP(&forceDelete, "yes", "y", false, "Skip confirmation prompt")
	markPrompts(c, "yes", "dry-run")
	stability.Mark(c, stability.Experimental, credentialsSince)
	return c
}
