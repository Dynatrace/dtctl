package cmd

import (
	"fmt"
	"math"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/exec"
	"github.com/dynatrace-oss/dtctl/pkg/output"
)

// bytesPerToken converts --max-output-tokens to a byte budget. Four bytes per
// token is the usual rule of thumb for English text and JSON under common
// tokenizers; the budget is deliberately approximate, not a tokenizer.
const bytesPerToken = 4

// outputBounds are the resolved in-response bounds on agent-mode query output.
type outputBounds struct {
	MaxFieldChars  int
	MaxOutputBytes int64
	// Warnings are for flags that were given but have no effect here.
	Warnings []string
}

// addOutputBoundFlags registers the agent-mode output bounds on a command.
func addOutputBoundFlags(cmd *cobra.Command) {
	cmd.Flags().Int("max-field-chars", 0, `clip string values longer than N chars in an agent-mode result; a clipped value ends in "…(+N chars)"
and context.truncated_fields names its field (default 500 in agent mode; 0 = full values; spilled files are never clipped)`)
	cmd.Flags().String("max-output-bytes", "", `bound the agent-mode output to this size of the encoded envelope, e.g. 16KB: the rows that fit are
returned and context.truncated/returned/next_offset/next mark the cut (default: no budget)`)
	cmd.Flags().Int("max-output-tokens", 0, "like --max-output-bytes, in approximate tokens (1 token ≈ 4 bytes)")
	cmd.MarkFlagsMutuallyExclusive("max-output-bytes", "max-output-tokens")
}

// resolveOutputBounds resolves --max-field-chars and --max-output-bytes/-tokens.
// Both bound what an agent receives inside the envelope, so they apply in agent
// mode only: outside it the output is unchanged and an explicit flag warns.
func resolveOutputBounds(cmd *cobra.Command) (outputBounds, error) {
	var b outputBounds
	f := cmd.Flags()

	chars, _ := f.GetInt("max-field-chars")
	if chars < 0 {
		return b, fmt.Errorf("--max-field-chars must not be negative (0 = full values)")
	}
	if !f.Changed("max-field-chars") {
		chars = output.DefaultAgentMaxFieldChars
	}

	var budget int64
	if raw, _ := f.GetString("max-output-bytes"); raw != "" {
		n, err := exec.ParseByteSize(raw)
		if err != nil {
			return b, fmt.Errorf("invalid --max-output-bytes: %w", err)
		}
		budget = n
	}
	tokens, _ := f.GetInt("max-output-tokens")
	if tokens < 0 {
		return b, fmt.Errorf("--max-output-tokens must not be negative")
	}
	if int64(tokens) > math.MaxInt64/bytesPerToken {
		return b, fmt.Errorf("--max-output-tokens %d is too large", tokens)
	}
	if tokens > 0 {
		budget = int64(tokens) * bytesPerToken
	}

	if !agentMode(cmdContext(cmd)) {
		if f.Changed("max-field-chars") || f.Changed("max-output-bytes") || f.Changed("max-output-tokens") {
			b.Warnings = append(b.Warnings, "--max-field-chars/--max-output-bytes/--max-output-tokens apply in agent mode only (--agent); the output is unchanged")
		}
		return b, nil
	}
	b.MaxFieldChars = chars
	b.MaxOutputBytes = budget
	return b, nil
}

// outputBoundsSince is the release that introduced the output-bound flags.
const outputBoundsSince = "0.40.0"
