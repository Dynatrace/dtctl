package cmd

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/dynatrace-oss/dtctl/pkg/exec"
	"github.com/dynatrace-oss/dtctl/pkg/output"
)

// windowArg finds a query that names its own window.
var windowArg = regexp.MustCompile(`\b(?:from|to|timeframe)\s*:`)

// queryNamesWindow reports whether a query sets from:, to: or timeframe:, ignoring comments and strings.
func queryNamesWindow(dql string) bool { return windowArg.MatchString(dqlCode(dql)) }

// queryWindowContext describes the window a query searched; a query naming none reads the default 2h, which the note makes visible.
func queryWindowContext(result *exec.DQLQueryResponse, named bool) *output.TimeWindow {
	g := result.GetMetadata()
	if g == nil || g.AnalysisTimeframe == nil || g.AnalysisTimeframe.Start == "" {
		return nil
	}
	w := &output.TimeWindow{From: g.AnalysisTimeframe.Start, To: g.AnalysisTimeframe.End}
	start, err1 := time.Parse(time.RFC3339, w.From)
	end, err2 := time.Parse(time.RFC3339, w.To)
	if err1 != nil || err2 != nil || !end.After(start) {
		return w
	}
	w.Span = spanString(end.Sub(start))
	if !named {
		w.Note = "the query names no window, so it read the default last " + w.Span + "; widen it with fetch ..., from: now()-24h"
	}
	return w
}

// spanString is d in the largest whole unit, days from 2d on: 7d, 24h, 15m.
func spanString(d time.Duration) string {
	d = d.Round(time.Minute)
	switch {
	case d >= 48*time.Hour && d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0 && d > 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return d.String()
}

// dqlCode is dql without comments and quoted contents, so matches see only code.
func dqlCode(dql string) string {
	var b strings.Builder
	for _, line := range strings.Split(dql, "\n") {
		var quote byte
		for i := 0; i < len(line); i++ {
			c := line[i]
			if quote != 0 {
				if c == '\\' && quote == '"' {
					i++
				} else if c == quote {
					quote = 0
					b.WriteByte(c)
				}
				continue
			}
			if c == '/' && i+1 < len(line) && line[i+1] == '/' {
				break
			}
			if c == '"' || c == '`' {
				quote = c
			}
			b.WriteByte(c)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
