package cmd

import (
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

// dataNouns are data domains agents guess as top-level commands, each with a DQL starting point.
var dataNouns = map[string]string{
	"dql":             `dtctl query 'fetch logs, from:now()-1h | limit 10'`,
	"sql":             `dtctl query 'fetch logs, from:now()-1h | limit 10'`,
	"log":             `dtctl query 'fetch logs, from:now()-1h | limit 10'`,
	"event":           `dtctl query 'fetch events, from:now()-24h | summarize count(), by:{event.kind}'`,
	"events":          `dtctl query 'fetch events, from:now()-24h | summarize count(), by:{event.kind}'`,
	"bizevents":       `dtctl query 'fetch bizevents, from:now()-24h | summarize count(), by:{event.type}'`,
	"businessevents":  `dtctl query 'fetch bizevents, from:now()-24h | summarize count(), by:{event.type}'`,
	"business-events": `dtctl query 'fetch bizevents, from:now()-24h | summarize count(), by:{event.type}'`,
	"span":            `dtctl query 'fetch spans, from:now()-1h | limit 10'`,
	"spans":           `dtctl query 'fetch spans, from:now()-1h | limit 10'`,
	"trace":           `dtctl query 'fetch spans, from:now()-1h | limit 10'`,
	"traces":          `dtctl query 'fetch spans, from:now()-1h | limit 10'`,
	"metric":          `dtctl query 'fetch metric.series, from:now()-1h | limit 10'`,
	"metrics":         `dtctl query 'fetch metric.series, from:now()-1h | limit 10'`,
	"synthetic":       `dtctl query 'fetch dt.synthetic.events, from:now()-24h | limit 10'`,
	"synthetics":      `dtctl query 'fetch dt.synthetic.events, from:now()-24h | limit 10'`,
	"synmon":          `dtctl query 'fetch dt.synthetic.events, from:now()-24h | limit 10'`,
	"monitor":         `dtctl query 'fetch dt.synthetic.events, from:now()-24h | limit 10'`,
	"monitors":        `dtctl query 'fetch dt.synthetic.events, from:now()-24h | limit 10'`,
	"security":        `dtctl query 'fetch security.events, from:now()-24h | limit 10'`,
	"vulnerability":   `dtctl query 'fetch security.events, from:now()-24h | limit 10'`,
	"vulnerabilities": `dtctl query 'fetch security.events, from:now()-24h | limit 10'`,
	"problem":         `dtctl query 'fetch dt.davis.problems, from:now()-24h | limit 10'`,
	"problems":        `dtctl query 'fetch dt.davis.problems, from:now()-24h | limit 10'`,
	"service":         `dtctl query 'smartscapeNodes "SERVICE" | limit 10'`,
	"services":        `dtctl query 'smartscapeNodes "SERVICE" | limit 10'`,
	"host":            `dtctl query 'smartscapeNodes "HOST" | limit 10'`,
	"hosts":           `dtctl query 'smartscapeNodes "HOST" | limit 10'`,
}

// nounAdvice returns the commands that read a noun, or nil. It reads root, the
// tree the invocation runs, not the singleton: an invocation on a tree of its
// own must not walk (and so lazily sort) another one's.
func nounAdvice(root *cobra.Command, name string) []string {
	n := strings.ToLower(name)
	var out []string
	named := getResourcesNamed(root, n)
	// A guessed compound ("slo-status") names its resource in its first word.
	if head, _, found := strings.Cut(n, "-"); found && len(named) == 0 {
		named = getResourcesNamed(root, head)
	}
	for _, r := range named {
		out = append(out, "dtctl get "+r)
	}
	if q, ok := dataNouns[n]; ok {
		out = append(out, q)
	}
	if len(out) == 0 {
		return nil
	}
	return append(out, "dtctl commands  # the full catalog")
}

// getResourcesNamed returns the get resources a noun names, or prefixes.
func getResourcesNamed(root *cobra.Command, n string) []string {
	var get *cobra.Command
	for _, c := range root.Commands() {
		if c.Name() == "get" {
			get = c
		}
	}
	if get == nil {
		return nil
	}
	forms := []string{n, n + "s", strings.TrimSuffix(n, "s")}
	stem := strings.TrimSuffix(n, "s") + "-"
	names := func(c *cobra.Command) []string { return append([]string{c.Name()}, c.Aliases...) }
	var exact, prefixed []string
	for _, c := range get.Commands() {
		if !c.IsAvailableCommand() {
			continue
		}
		switch {
		case slices.ContainsFunc(names(c), func(a string) bool { return slices.Contains(forms, a) }):
			exact = append(exact, c.Name())
		case strings.HasPrefix(c.Name(), stem):
			prefixed = append(prefixed, c.Name())
		}
	}
	exact = append(exact, prefixed...)
	return exact[:min(len(exact), 2)]
}
