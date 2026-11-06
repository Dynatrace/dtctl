package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dynatrace/dtctl/pkg/auth"
	"github.com/Dynatrace/dtctl/pkg/output"
	"github.com/Dynatrace/dtctl/pkg/resources/credentialvault"
	"github.com/Dynatrace/dtctl/pkg/safety"
	"github.com/Dynatrace/dtctl/pkg/stability"
	"github.com/Dynatrace/dtctl/pkg/util/format"
)

// createCredentialCmd creates a credential vault entry.
var createCredentialCmd = newCreateCredentialCmd()

func newCreateCredentialCmd() *cobra.Command {
	var (
		file, typ, name, description, user string
		certFile, certFormat               string
		passwordFile, tokenFile            string
		scopes                             []string
		ownerAccessOnly                    bool
	)
	c := &cobra.Command{
		Use:     "credential --type <type> --name <name> --scope <scope> [secret flags]",
		Aliases: []string{"credentials", "cred"},
		Short:   "Create a credential vault entry",
		Long: `Create a credential vault entry.

Secret material (certificates, passwords, tokens) is read from files or stdin
("-"), never from flag values, and is never printed.

Types and their inputs:
  public-certificate   --certificate-file (PEM)
  certificate          --certificate-file (PEM or PKCS#12) [--password-file for PKCS#12]
  token                --token-file
  username-password    --user, --password-file

Other types can be created from a full definition with -f.

Examples:
  # Trust a CA certificate for extension signing
  dtctl create credential --type public-certificate --scope extension \
    --name my-extension-ca --certificate-file ca.pem

  # Read the token from stdin
  printf '%s' "$TOKEN" | dtctl create credential --type token --scope extension-authentication \
    --name my-token --token-file -

  # Create from a YAML/JSON definition (API field names)
  dtctl create credential -f credential.yaml

  # Preview without creating
  dtctl create credential --type public-certificate --scope extension \
    --name my-extension-ca --certificate-file ca.pem --dry-run
`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmdContext(cmd)
			var req credentialvault.CredentialRequest

			if file != "" {
				var err error
				if req, err = readCredentialDefinition(cmd, file, []string{"type", "name", "scope", "description", "owner-access-only",
					"certificate-file", "certificate-format", "password-file", "token-file", "user"}); err != nil {
					return err
				}
				if err := credentialvault.NormalizeRequest(&req); err != nil {
					return err
				}
			} else {
				if typ == "" {
					return fmt.Errorf("--type is required (or use -f to specify a file)")
				}
				t, err := credentialvault.NormalizeType(typ)
				if err != nil {
					return err
				}
				normScopes, err := credentialvault.NormalizeScopes(scopes)
				if err != nil {
					return err
				}
				in, err := readCredentialSecrets(ctx, certFile, passwordFile, tokenFile, certFormat, user)
				if err != nil {
					return err
				}
				req, err = credentialvault.BuildRequest(name, t, description, normScopes, ownerAccessOnly, in)
				if err != nil {
					return err
				}
			}

			if dryRun(ctx) {
				// Never include secret material (or the request payload) in the preview.
				return credentialDryRunTarget(cmd, newDryRunReport(cmd).Linef("Dry run: would create credential")).
					Field("Name", "%s", req.Name).
					Field("Type", "%s", req.Type).
					Field("Scopes", "%s", strings.Join(req.Scopes, ", ")).
					Field("Owner Access Only", "%v", req.OwnerAccessOnly).
					Print()
			}

			_, c, err := setupWithSafety(ctx, safety.OperationCreate)
			if err != nil {
				return err
			}

			id, err := credentialvault.NewHandler(c).Create(req)
			if err != nil {
				if isPermissionDenied(err) {
					return credentialAccessDenied(err, "create credential", auth.AccessWrite)
				}
				return fmt.Errorf("failed to create credential: %w", err)
			}
			output.FprintSuccess(currentStderr(ctx), "Credential %q created (ID: %s)", req.Name, id)
			return nil
		},
	}
	c.Flags().StringVarP(&file, "file", "f", "", "file containing a credential definition (YAML/JSON, API field names), or - for stdin")
	markFileFlag(c, "file", fileValuePath)
	c.Flags().StringVar(&typ, "type", "", "credential type: public-certificate, certificate, token, username-password")
	c.Flags().StringVar(&name, "name", "", "credential name")
	c.Flags().StringVar(&description, "description", "", "credential description")
	c.Flags().StringSliceVar(&scopes, "scope", nil, "credential scope, repeatable: extension (extension signing CAs), extension-authentication (secrets an extension uses), synthetic, app-engine")
	c.Flags().BoolVar(&ownerAccessOnly, "owner-access-only", false, "make the credential available to its owner only")
	c.Flags().StringVar(&certFile, "certificate-file", "", "certificate file (PEM, or PKCS#12 for type certificate), or - for stdin")
	markFileFlag(c, "certificate-file", fileValuePath)
	c.Flags().StringVar(&certFormat, "certificate-format", "", "certificate format for type certificate: pem or pkcs12 (default: detect)")
	c.Flags().StringVar(&passwordFile, "password-file", "", "file containing the password, or - for stdin")
	markFileFlag(c, "password-file", fileValuePath)
	c.Flags().StringVar(&tokenFile, "token-file", "", "file containing the token, or - for stdin")
	markFileFlag(c, "token-file", fileValuePath)
	c.Flags().StringVar(&user, "user", "", "username for type username-password")
	rejectEmptyFlag(c, "name")
	stability.Mark(c, stability.Experimental, credentialsSince)
	return c
}

// credentialDryRunTarget adds the context and environment a credential write
// would land in: the vault is environment-wide.
func credentialDryRunTarget(cmd *cobra.Command, report *dryRunReport) *dryRunReport {
	if cfg, err := loadConfig(cmdContext(cmd)); err == nil {
		if cur, err := cfg.CurrentContextObj(); err == nil {
			report = report.Field("Context", "%s", cfg.CurrentContext).Field("Environment", "%s", cur.Environment)
		}
	}
	return report
}

// readCredentialDefinition reads a -f credential definition (YAML or JSON, API
// field names), refusing any of the conflicting flags.
func readCredentialDefinition(cmd *cobra.Command, file string, conflicting []string) (credentialvault.CredentialRequest, error) {
	var req credentialvault.CredentialRequest
	data, err := readFileFlag(cmdContext(cmd), "file", file)
	if err != nil {
		return req, fmt.Errorf("failed to read file: %w", err)
	}
	for _, f := range conflicting {
		if cmd.Flags().Changed(f) {
			return req, fmt.Errorf("-f cannot be combined with --%s; put the value in the definition file", f)
		}
	}
	jsonData, err := format.ValidateAndConvert(data)
	if err != nil {
		return req, fmt.Errorf("invalid file format: %w", err)
	}
	if err := json.Unmarshal(jsonData, &req); err != nil {
		return req, fmt.Errorf("failed to parse credential definition: %w", err)
	}
	return req, nil
}

// readCredentialSecrets reads the secret flags' files (or stdin, for at most
// one of them).
func readCredentialSecrets(ctx context.Context, certFile, passwordFile, tokenFile, certFormat, user string) (credentialvault.SecretInput, error) {
	in := credentialvault.SecretInput{CertificateFormat: certFormat, User: user}
	stdinUses := 0
	for _, p := range []string{certFile, passwordFile, tokenFile} {
		if p == "-" {
			stdinUses++
		}
	}
	if stdinUses > 1 {
		return in, fmt.Errorf("only one of --certificate-file, --password-file, --token-file can read stdin")
	}
	var err error
	if certFile != "" {
		if in.Certificate, err = readFileFlag(ctx, "certificate-file", certFile); err != nil {
			return in, fmt.Errorf("failed to read certificate: %w", err)
		}
	}
	if passwordFile != "" {
		if in.Password, err = readFileFlag(ctx, "password-file", passwordFile); err != nil {
			return in, fmt.Errorf("failed to read password: %w", err)
		}
	}
	if tokenFile != "" {
		if in.Token, err = readFileFlag(ctx, "token-file", tokenFile); err != nil {
			return in, fmt.Errorf("failed to read token: %w", err)
		}
	}
	return in, nil
}
