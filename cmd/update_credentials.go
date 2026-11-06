package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dynatrace/dtctl/pkg/auth"
	"github.com/Dynatrace/dtctl/pkg/output"
	"github.com/Dynatrace/dtctl/pkg/resources/credentialvault"
	"github.com/Dynatrace/dtctl/pkg/safety"
	"github.com/Dynatrace/dtctl/pkg/stability"
)

// updateCredentialCmd replaces the secret (and optionally metadata) of a
// credential vault entry in place, keeping its ID.
var updateCredentialCmd = newUpdateCredentialCmd()

func newUpdateCredentialCmd() *cobra.Command {
	var (
		file, name, description, user string
		certFile, certFormat          string
		passwordFile, tokenFile       string
		scopes                        []string
		ownerAccessOnly               bool
	)
	c := &cobra.Command{
		Use:     "credential <id> [secret flags]",
		Aliases: []string{"credentials", "cred"},
		Short:   "Rotate the secret of a credential vault entry, keeping its ID",
		Long: `Rotate the secret of a credential vault entry in place, so everything that
references the entry by ID keeps working.

The API replaces the whole entry and never returns secret material, so every
update must supply the secret again: --certificate-file, --token-file or
--password-file (with --user), as for 'create credential'. Name, description,
scopes, owner-only access, allowed entities and contextless access are kept
from the current entry unless a flag changes them. The type cannot change.

Secret material is read from files or stdin ("-"), never from flag values, and
is never printed.

Updating an entry owned by another user needs the
credential-vault:entries:admin scope; under readwrite-mine only your own
entries can be updated.

Examples:
  # Replace an extension signing CA (a new certificate, same ID)
  dtctl update credential CREDENTIAL_VAULT_ENTRY-0123456789ABCDEF --certificate-file new-ca.pem

  # Rotate a token from stdin
  printf '%s' "$TOKEN" | dtctl update credential CREDENTIAL_VAULT_ENTRY-0123456789ABCDEF --token-file -

  # Rotate and rename
  dtctl update credential CREDENTIAL_VAULT_ENTRY-0123456789ABCDEF --token-file token.txt --name ci-token-2027

  # Replace from a full definition (API field names)
  dtctl update credential CREDENTIAL_VAULT_ENTRY-0123456789ABCDEF -f credential.yaml

  # Preview without updating
  dtctl update credential CREDENTIAL_VAULT_ENTRY-0123456789ABCDEF --certificate-file new-ca.pem --dry-run
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmdContext(cmd)
			id := args[0]

			// Read the inputs first: they fail fast and need no API call.
			var (
				req credentialvault.CredentialRequest
				in  credentialvault.SecretInput
				err error
			)
			if file != "" {
				if req, err = readCredentialDefinition(cmd, file, []string{"name", "scope", "description", "owner-access-only",
					"certificate-file", "certificate-format", "password-file", "token-file", "user"}); err != nil {
					return err
				}
			} else {
				if in, err = readCredentialSecrets(ctx, certFile, passwordFile, tokenFile, certFormat, user); err != nil {
					return err
				}
				if len(in.Certificate) == 0 && len(in.Token) == 0 && len(in.Password) == 0 {
					return fmt.Errorf("the API replaces the whole entry and never returns its secret, so an update must supply it again " +
						"(--certificate-file, --token-file or --password-file), or use -f with a full definition")
				}
			}

			cfg, c, err := setupClient(ctx)
			if err != nil {
				return err
			}
			handler := credentialvault.NewHandler(c)
			cur, err := handler.Get(id)
			if err != nil {
				return credentialAccessDenied(err, "get credential", auth.AccessRead)
			}

			if file != "" {
				if req.Type == "" {
					req.Type = cur.Type
				}
				if err := credentialvault.NormalizeRequest(&req); err != nil {
					return err
				}
				if req.Type != cur.Type {
					return fmt.Errorf("the type of a credential cannot change (%s → %s); create a new credential instead", cur.Type, req.Type)
				}
			} else {
				if !cmd.Flags().Changed("name") {
					name = cur.Name
				}
				if !cmd.Flags().Changed("description") {
					description = cur.Description
				}
				if !cmd.Flags().Changed("owner-access-only") {
					ownerAccessOnly = cur.OwnerAccessOnly
				}
				normScopes := cur.Scopes
				if cmd.Flags().Changed("scope") {
					if normScopes, err = credentialvault.NormalizeScopes(scopes); err != nil {
						return err
					}
				}
				if req, err = credentialvault.BuildRequest(name, cur.Type, description, normScopes, ownerAccessOnly, in); err != nil {
					return err
				}
				// PUT replaces the entry: carry over the access settings no flag sets.
				req.AllowedEntities = cur.AllowedEntities
				if cur.AllowContextlessRequests {
					allow := true
					req.AllowContextlessRequests = &allow
				}
			}

			if dryRun(ctx) {
				// Never include secret material (or the request payload) in the preview.
				return credentialDryRunTarget(cmd, newDryRunReport(cmd).Linef("Dry run: would update credential %s", id)).
					Field("Name", "%s", changed(cur.Name, req.Name)).
					Field("Type", "%s", req.Type).
					Field("Scopes", "%s", changed(strings.Join(cur.Scopes, ", "), strings.Join(req.Scopes, ", "))).
					Field("Owner Access Only", "%s", changed(fmt.Sprint(cur.OwnerAccessOnly), fmt.Sprint(req.OwnerAccessOnly))).
					Field("Secret", "%s", "replaced").
					Print()
			}

			if err := checkSafety(ctx, cfg, safety.OperationUpdate, credentialOwnership(c, cur.Owner)); err != nil {
				return err
			}
			if err := handler.Update(id, req); err != nil {
				if isPermissionDenied(err) {
					return credentialAccessDenied(err, "update credential", auth.AccessWrite)
				}
				return fmt.Errorf("failed to update credential: %w", err)
			}
			output.FprintSuccess(currentStderr(ctx), "Credential %q updated (ID: %s)", req.Name, id)
			return nil
		},
	}
	c.Flags().StringVarP(&file, "file", "f", "", "file containing the full credential definition (YAML/JSON, API field names), or - for stdin")
	markFileFlag(c, "file", fileValuePath)
	c.Flags().StringVar(&name, "name", "", "new credential name (default: keep)")
	c.Flags().StringVar(&description, "description", "", "new description (default: keep)")
	c.Flags().StringSliceVar(&scopes, "scope", nil, "replace the scopes, repeatable: extension (extension signing CAs), extension-authentication (secrets an extension uses), synthetic, app-engine (default: keep)")
	c.Flags().BoolVar(&ownerAccessOnly, "owner-access-only", false, "make the credential available to its owner only (default: keep)")
	c.Flags().StringVar(&certFile, "certificate-file", "", "new certificate file (PEM, or PKCS#12 for type certificate), or - for stdin")
	markFileFlag(c, "certificate-file", fileValuePath)
	c.Flags().StringVar(&certFormat, "certificate-format", "", "certificate format for type certificate: pem or pkcs12 (default: detect)")
	c.Flags().StringVar(&passwordFile, "password-file", "", "file containing the new password, or - for stdin")
	markFileFlag(c, "password-file", fileValuePath)
	c.Flags().StringVar(&tokenFile, "token-file", "", "file containing the new token, or - for stdin")
	markFileFlag(c, "token-file", fileValuePath)
	c.Flags().StringVar(&user, "user", "", "username for type username-password")
	rejectEmptyFlag(c, "name")
	stability.Mark(c, stability.Experimental, credentialsSince)
	return c
}

// changed renders "old → new" when a value changes, or the value alone.
func changed(old, new string) string {
	if old == new {
		return new
	}
	return old + " → " + new
}
