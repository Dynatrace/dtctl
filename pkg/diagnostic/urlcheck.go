package diagnostic

import (
	"strings"

	"github.com/Dynatrace/dtctl/sdk/urls"
)

// URLProblem describes a detected issue with an environment URL.
// Alias for urls.Problem from the SDK.
type URLProblem = urls.Problem

// CheckEnvironmentURL inspects an environment URL for common mistakes.
// Delegates to urls.Check from the SDK.
func CheckEnvironmentURL(environmentURL string) []URLProblem {
	return urls.Check(environmentURL)
}

// URLSuggestions returns troubleshooting suggestions based on URL problems.
// This adds the dtctl-specific "Update with: dtctl ctx set" hint.
func URLSuggestions(environmentURL string) []string {
	suggestions := urls.Suggestions(environmentURL)
	if len(suggestions) > 0 {
		suggestions = append(suggestions, "Update with: dtctl ctx set <name> --environment <correct-url>")
	}
	return suggestions
}

// fixDomain replaces oldSuffix with newSuffix in the URL, case-insensitively.
// Kept for test compatibility.
func fixDomain(rawURL, oldSuffix, newSuffix string) string {
	lower := strings.ToLower(rawURL)
	idx := strings.Index(lower, strings.ToLower(oldSuffix))
	if idx < 0 {
		return rawURL
	}
	return rawURL[:idx] + newSuffix + rawURL[idx+len(oldSuffix):]
}
