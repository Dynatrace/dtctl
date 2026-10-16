package cmd

import (
	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/stability"
)

func init() {
	rootCmd.AddCommand(tokenScopesHelpTopicCmd)
}

// tokenScopesHelpTopicCmd is a help-topic-only command (no Run function).
// It makes "dtctl help token-scopes" work as advertised in error messages.
var tokenScopesHelpTopicCmd = newTokenScopesHelpTopicCmd()

func newTokenScopesHelpTopicCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "token-scopes",
		Short: "Required token scopes for each safety level",
		Long: `Token Scopes Reference

Lists the Dynatrace platform token scopes required for each dtctl safety level.
Copy the scope list for your desired safety level when creating a token in Dynatrace.

Note: Safety levels are client-side only. The token scopes you configure in
Dynatrace are what actually controls access. Configure your tokens with the
minimum required scopes for your use case.

Quick Reference:

  Safety Level                Use Case                                Token Type
  readonly                    Production monitoring, troubleshooting  Read-only token
  readwrite-mine              Personal development, sandbox           Standard token
  readwrite-all               Team environments, administration       Standard token
  dangerously-unrestricted    Dev environments, bucket management     Full access token

For the full list of scopes per safety level, see:
  https://github.com/dynatrace-oss/dtctl/blob/main/docs/TOKEN_SCOPES.md

For creating platform tokens, see:
  https://docs.dynatrace.com/docs/manage/identity-access-management/access-tokens-and-oauth-clients/platform-tokens`,
	}
	stability.MarkStable(c)
	return c
}

// Declared stable: the invocation and output contract of this command is
// additive-only. Stable is never implied -- see AGENTS.md "Stability Tiers".
func init() {
}
