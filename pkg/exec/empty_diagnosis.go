package exec

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/suggest"
)

// Empty-result diagnosis (#579).
//
// A typo in a field name or a metric key makes DQL succeed with zero rows, and
// the only advice an agent used to get was "widen the window" — so it re-ran
// the same broken query over 7 days, paid for the bigger scan, and concluded
// that nothing existed. On an empty result dtctl now runs at most one small,
// bounded probe per query shape and reports what it saw:
//
//   - fetch queries: sample the first records of the user's own fetch stage and
//     compare the field names referenced in filter/by: against the sample.
//   - timeseries queries: list the metric keys that reported series in (the
//     last 2h of) the query window and look for the queried key.
//
// A finding is only structured (context.empty_reason) and only replaces the
// widen-the-window advice when there is a near match: that is the typo
// signature. Every finding carries its basis — a sample of N records, or the
// checked window — because absence from a sample is an observation, not a
// catalog fact. Any probe failure, partial probe result or empty sample leaves
// the advice exactly as it was without diagnosis.

const (
	// emptyProbeSampleRecords bounds the field-name sample. With `| limit` right
	// after the fetch, Grail stops scanning once it has this many records.
	emptyProbeSampleRecords = 100
	// emptyProbeMaxBytes caps each probe's result size.
	emptyProbeMaxBytes = 4 * 1000 * 1000
	// emptyProbeScanLimitGbytes caps the data a probe may scan.
	emptyProbeScanLimitGbytes = 1
	// emptyProbeFetchTimeoutSeconds caps each probe's server-side read time.
	emptyProbeFetchTimeoutSeconds = 10
	// emptyProbeBudget bounds the wall-clock time all probes may add.
	emptyProbeBudget = 20 * time.Second
	// emptyProbeMetricWindow is the longest window the metric-key probe lists.
	emptyProbeMetricWindow = 2 * time.Hour
	// emptyProbeMaxMetricKeys caps the metric key listing; a listing that hits
	// the cap is treated as partial and proves nothing.
	emptyProbeMaxMetricKeys = 50000
)

// probeFunc runs one diagnostic query. It matches ExecuteQueryWithContext so
// tests can substitute a fake.
type probeFunc func(ctx context.Context, query string, opts DQLExecuteOptions) (*DQLQueryResponse, error)

func (e *DQLExecutor) probeRunner() probeFunc {
	if e.probe != nil {
		return e.probe
	}
	if e.sdk == nil {
		return nil
	}
	return e.ExecuteQueryWithContext
}

// emptyResultAdvice returns the empty-result diagnosis and the suggestion lines
// for the agent envelope. On a non-empty result it returns nothing and runs no
// probe. When no near-match finding explains the emptiness, the suggestions
// include today's windowAdvice unchanged. A result cut short by a limit is
// not diagnosed: the limit may be why it is empty. The diagnosis reads
// opts.TypedQuery when it is set, not the query that was sent.
func (e *DQLExecutor) emptyResultAdvice(query string, result *DQLQueryResponse, records []map[string]interface{}, opts DQLExecuteOptions) (*output.EmptyReason, []string) {
	if !isEmptyResult(records) {
		return nil, nil
	}
	if opts.TypedQuery != "" {
		query = opts.TypedQuery
	}
	var reason *output.EmptyReason
	var suggestions []string
	if probe := e.probeRunner(); probe != nil && (result == nil || !probeIsPartial(result)) {
		ctx, cancel := context.WithTimeout(context.Background(), emptyProbeBudget)
		defer cancel()
		if keys := queriedMetricKeys(result); len(keys) > 0 {
			reason, suggestions = diagnoseMetricKeys(ctx, probe, keys, result, opts)
		} else if obj, fields := referencedFields(query); obj != "" && len(fields) > 0 {
			reason, suggestions = diagnoseFields(ctx, probe, query, obj, fields, opts)
		}
	}
	if reason == nil {
		suggestions = append(suggestions, windowAdvice(query, records, opts)...)
	}
	return reason, suggestions
}

// baseProbeOptions bounds a probe's scan, result size and read time. The
// probes never emit progress and carry their own client context so they are
// attributable in query consumption.
func baseProbeOptions(opts DQLExecuteOptions) DQLExecuteOptions {
	return DQLExecuteOptions{
		MaxResultBytes:         emptyProbeMaxBytes,
		DefaultScanLimitGbytes: emptyProbeScanLimitGbytes,
		FetchTimeoutSeconds:    emptyProbeFetchTimeoutSeconds,
		Locale:                 opts.Locale,
		Timezone:               opts.Timezone,
		ClientContext:          "empty-result-diagnosis",
	}
}

// probeIsPartial reports whether a probe result was cut short.
func probeIsPartial(r *DQLQueryResponse) bool {
	for _, n := range r.GetNotifications() {
		if ResultIsPartial(n) {
			return true
		}
	}
	return false
}

// diagnoseFields samples the user's fetch stage and reports referenced fields
// that occur in none of the sampled records.
func diagnoseFields(ctx context.Context, probe probeFunc, query, object string, fields []string, opts DQLExecuteOptions) (*output.EmptyReason, []string) {
	stages := splitStages(query)
	probeOpts := baseProbeOptions(opts)
	probeOpts.MaxResultRecords = emptyProbeSampleRecords
	// The sample covers the same window as the user's query: the fetch stage is
	// reused verbatim (with its from:/bucket: parameters) and the default
	// timeframe flags are passed through.
	probeOpts.DefaultTimeframeStart = opts.DefaultTimeframeStart
	probeOpts.DefaultTimeframeEnd = opts.DefaultTimeframeEnd
	resp, err := probe(ctx, fmt.Sprintf("%s | limit %d", stages[0], emptyProbeSampleRecords), probeOpts)
	// A sample cut short by a limit is skewed toward whatever was read first,
	// so it is not used as evidence.
	if err != nil || resp == nil || probeIsPartial(resp) {
		return nil, nil
	}
	sample := resp.GetRecords()
	if len(sample) == 0 {
		return nil, nil
	}
	seen := map[string]bool{}
	var keys []string
	for _, rec := range sample {
		for k := range rec {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}

	var reason *output.EmptyReason
	var suggestions []string
	n := len(sample)
	for _, f := range fields {
		if seen[f] {
			continue
		}
		near := nearMatches(f, keys)
		if len(near) == 0 {
			suggestions = append(suggestions, fmt.Sprintf("# `%s` did not occur in any of the %d sampled `%s` records — check the field name (it may also be a rare field absent from the sample)", f, n, object))
			continue
		}
		suggestions = append(suggestions, fmt.Sprintf("# `%s` did not occur in any of the %d sampled `%s` records, but %s did — likely a typo in the field name; a filter on an absent field matches nothing, so fix the name before widening the time window", f, n, object, backtickList(near)))
		if reason == nil {
			reason = &output.EmptyReason{
				Code:       "field_not_in_sample",
				Field:      f,
				DataObject: object,
				DidYouMean: near,
				SampleSize: n,
				Evidence:   fmt.Sprintf("`%s` is absent from all %d sampled `%s` records (the query's fetch stage with `| limit %d`); the near match is present in the sample", f, n, object, emptyProbeSampleRecords),
			}
		}
	}
	return reason, suggestions
}

// queriedMetricKeys returns the metric keys Grail reports for a timeseries
// query, in order and without duplicates.
func queriedMetricKeys(result *DQLQueryResponse) []string {
	if result == nil {
		return nil
	}
	seen := map[string]bool{}
	var keys []string
	for _, m := range result.GetMetrics() {
		if m.MetricKey != "" && !seen[m.MetricKey] {
			seen[m.MetricKey] = true
			keys = append(keys, m.MetricKey)
		}
	}
	return keys
}

// diagnoseMetricKeys lists the metric keys with series in the query window
// (clamped to its last emptyProbeMetricWindow) and reports queried keys that
// are not among them.
func diagnoseMetricKeys(ctx context.Context, probe probeFunc, keys []string, result *DQLQueryResponse, opts DQLExecuteOptions) (*output.EmptyReason, []string) {
	meta := result.GetMetadata()
	if meta == nil || meta.AnalysisTimeframe == nil {
		return nil, nil
	}
	start, err1 := time.Parse(time.RFC3339, meta.AnalysisTimeframe.Start)
	end, err2 := time.Parse(time.RFC3339, meta.AnalysisTimeframe.End)
	if err1 != nil || err2 != nil || !end.After(start) {
		return nil, nil
	}
	if end.Sub(start) > emptyProbeMetricWindow {
		start = end.Add(-emptyProbeMetricWindow)
	}
	from, to := start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339)

	probeOpts := baseProbeOptions(opts)
	probeOpts.MaxResultRecords = emptyProbeMaxMetricKeys
	probeOpts.DefaultTimeframeStart = from
	probeOpts.DefaultTimeframeEnd = to
	resp, err := probe(ctx, "metrics | summarize n = count(), by:{metric.key} | fields metric.key", probeOpts)
	if err != nil || resp == nil || probeIsPartial(resp) {
		return nil, nil
	}
	recs := resp.GetRecords()
	if len(recs) == 0 || len(recs) >= emptyProbeMaxMetricKeys {
		return nil, nil
	}
	known := map[string]bool{}
	var catalog []string
	for _, r := range recs {
		if k, ok := r["metric.key"].(string); ok && !known[k] {
			known[k] = true
			catalog = append(catalog, k)
		}
	}

	var reason *output.EmptyReason
	var suggestions []string
	for _, k := range keys {
		if known[k] {
			continue
		}
		near := nearMatches(k, catalog)
		if len(near) == 0 {
			suggestions = append(suggestions, fmt.Sprintf("# no series with metric key `%s` was reported between %s and %s — check the key (list the keys: dtctl query 'metrics | summarize n = count(), by:{metric.key}') or widen the window", k, from, to))
			continue
		}
		suggestions = append(suggestions, fmt.Sprintf("# no series with metric key `%s` was reported between %s and %s, but %s was — likely a typo in the metric key; fix it before widening the time window", k, from, to, backtickList(near)))
		if reason == nil {
			reason = &output.EmptyReason{
				Code:       "metric_not_in_window",
				Metric:     k,
				DidYouMean: near,
				Evidence:   fmt.Sprintf("no series with metric key `%s` between %s and %s (metric key listing via the `metrics` command); the near match has series in that window", k, from, to),
			}
		}
	}
	return reason, suggestions
}

// nearMatches returns the candidates closest to name by case-insensitive edit
// distance — only those at the smallest distance found, at most three. The
// distance ceiling is deliberately tight (1 for short names, 2 otherwise):
// a loose match on a sparse field would pass a guess off as a typo.
func nearMatches(name string, candidates []string) []string {
	maxDist := 2
	if len(name) <= 6 {
		maxDist = 1
	}
	lower := strings.ToLower(name)
	best := maxDist + 1
	var out []string
	for _, c := range candidates {
		if c == name {
			continue
		}
		d := suggest.LevenshteinDistance(lower, strings.ToLower(c))
		switch {
		case d < best:
			best, out = d, []string{c}
		case d == best:
			out = append(out, c)
		}
	}
	if best > maxDist {
		return nil
	}
	sort.Strings(out)
	if len(out) > 3 {
		out = out[:3]
	}
	return out
}

func backtickList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = "`" + n + "`"
	}
	return strings.Join(q, ", ")
}

// fetchStageRe matches a leading `fetch <data object>` stage.
var fetchStageRe = regexp.MustCompile(`^fetch\s+([A-Za-z_][A-Za-z0-9_.]*)\s*(,|$)`)

// referencedFields returns the data object of a `fetch` query and the field
// names its filter / filterOut stages and its first summarize/makeTimeseries
// by: clause read. It walks only stages that leave the record shape intact
// (filter, filterOut, sort, limit, dedup), so every name it returns refers to
// a field of the fetched records rather than one a pipeline stage created.
func referencedFields(query string) (string, []string) {
	stages := splitStages(query)
	if len(stages) == 0 {
		return "", nil
	}
	m := fetchStageRe.FindStringSubmatch(stages[0])
	if m == nil {
		return "", nil
	}
	var fields []string
	seen := map[string]bool{}
	add := func(names []string) {
		for _, n := range names {
			if !seen[n] {
				seen[n] = true
				fields = append(fields, n)
			}
		}
	}
walk:
	for _, st := range stages[1:] {
		cmd, rest := splitCommand(st)
		switch strings.ToLower(cmd) {
		case "filter", "filterout":
			add(exprIdentifiers(rest))
		case "sort", "limit", "dedup":
		case "summarize", "maketimeseries":
			add(byClauseIdentifiers(rest))
			break walk
		default:
			break walk
		}
	}
	return m[1], fields
}

// splitStages splits a DQL query on top-level pipes, ignoring pipes inside
// strings, brackets and `//` line comments. Comments are dropped from the
// returned stages, which are trimmed.
func splitStages(query string) []string {
	var stages []string
	var cur strings.Builder
	depth := 0
	for i := 0; i < len(query); i++ {
		c := query[i]
		switch {
		case c == '"' || c == '\'' || c == '`':
			j := skipQuoted(query, i)
			cur.WriteString(query[i:j])
			i = j - 1
		case c == '/' && i+1 < len(query) && query[i+1] == '/':
			for i < len(query) && query[i] != '\n' {
				i++
			}
			cur.WriteByte(' ')
		case c == '(' || c == '[' || c == '{':
			depth++
			cur.WriteByte(c)
		case c == ')' || c == ']' || c == '}':
			depth--
			cur.WriteByte(c)
		case c == '|' && depth == 0:
			stages = append(stages, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" || len(stages) > 0 {
		stages = append(stages, s)
	}
	return stages
}

// skipQuoted returns the index just past the quoted run starting at s[i],
// honoring backslash escapes. An unterminated run extends to the end.
func skipQuoted(s string, i int) int {
	q := s[i]
	for j := i + 1; j < len(s); j++ {
		if s[j] == '\\' && q != '`' {
			j++
			continue
		}
		if s[j] == q {
			return j + 1
		}
	}
	return len(s)
}

// splitCommand splits a stage into its command word and the rest.
func splitCommand(stage string) (string, string) {
	i := 0
	for i < len(stage) && isIdentChar(stage[i]) {
		i++
	}
	return stage[:i], stage[i:]
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentChar(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

var dqlKeywords = map[string]bool{"and": true, "or": true, "not": true, "xor": true, "true": true, "false": true, "null": true}

// exprIdentifiers returns the field names an expression references: bare
// (dotted) identifiers and backtick-quoted names. Function names, named
// parameters (`name:`), keywords, literals, `$parameters` and anything inside
// square brackets (array indices, subqueries) are skipped.
func exprIdentifiers(expr string) []string {
	var out []string
	for i := 0; i < len(expr); {
		c := expr[i]
		switch {
		case c == '"' || c == '\'':
			i = skipQuoted(expr, i)
		case c == '`':
			j := skipQuoted(expr, i)
			if name := strings.TrimSuffix(expr[i+1:j], "`"); name != "" {
				out = append(out, name)
			}
			i = j
		case c == '[':
			depth := 0
			for ; i < len(expr); i++ {
				if expr[i] == '"' || expr[i] == '\'' || expr[i] == '`' {
					i = skipQuoted(expr, i) - 1
					continue
				}
				if expr[i] == '[' {
					depth++
				} else if expr[i] == ']' {
					depth--
					if depth == 0 {
						i++
						break
					}
				}
			}
		case c == '$' || (c >= '0' && c <= '9'):
			i++
			for i < len(expr) && (isIdentChar(expr[i]) || expr[i] == '.') {
				i++
			}
		case isIdentStart(c):
			j := i
			for j < len(expr) && (isIdentChar(expr[j]) || expr[j] == '.') {
				j++
			}
			name := strings.TrimRight(expr[i:j], ".")
			k := j
			for k < len(expr) && (expr[k] == ' ' || expr[k] == '\t' || expr[k] == '\n' || expr[k] == '\r') {
				k++
			}
			isCall := k < len(expr) && expr[k] == '('
			isParam := k < len(expr) && expr[k] == ':'
			if !isCall && !isParam && !dqlKeywords[strings.ToLower(name)] {
				out = append(out, name)
			}
			i = j
		default:
			i++
		}
	}
	return out
}

// byClauseIdentifiers returns the field names read by a top-level `by:`
// parameter. For an aliased grouping (`name = expr`) only expr is read.
func byClauseIdentifiers(params string) []string {
	idx := topLevelIndex(params, "by:")
	if idx < 0 {
		return nil
	}
	v := strings.TrimSpace(params[idx+len("by:"):])
	var body string
	if strings.HasPrefix(v, "{") {
		end := matchingClose(v, 0)
		body = v[1:end]
	} else {
		body = v
		if c := topLevelIndex(v, ","); c >= 0 {
			body = v[:c]
		}
	}
	var out []string
	for _, item := range splitTopLevel(body, ',') {
		if eq := aliasIndex(item); eq >= 0 {
			item = item[eq+1:]
		}
		out = append(out, exprIdentifiers(item)...)
	}
	return out
}

// topLevelIndex finds sub in s outside strings and brackets; -1 when absent.
// A sub that starts like an identifier (e.g. "by:") must not continue one.
func topLevelIndex(s, sub string) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' || c == '\'' || c == '`':
			i = skipQuoted(s, i) - 1
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			depth--
		case depth == 0 && strings.HasPrefix(s[i:], sub) && (i == 0 || !isIdentChar(sub[0]) || !isIdentChar(s[i-1])):
			return i
		}
	}
	return -1
}

// matchingClose returns the index of the bracket closing s[open], or len(s).
func matchingClose(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' || c == '\'' || c == '`':
			i = skipQuoted(s, i) - 1
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return len(s)
}

func splitTopLevel(s string, sep byte) []string {
	var parts []string
	for {
		i := topLevelIndex(s, string(sep))
		if i < 0 {
			return append(parts, s)
		}
		parts = append(parts, s[:i])
		s = s[i+1:]
	}
}

// aliasIndex returns the index of a top-level assignment `=` (not part of
// ==, !=, <=, >=) in s, or -1.
func aliasIndex(s string) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' || c == '\'' || c == '`':
			i = skipQuoted(s, i) - 1
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			depth--
		case c == '=' && depth == 0:
			prev := byte(0)
			if i > 0 {
				prev = s[i-1]
			}
			next := byte(0)
			if i+1 < len(s) {
				next = s[i+1]
			}
			if next != '=' && prev != '=' && prev != '!' && prev != '<' && prev != '>' {
				return i
			}
		}
	}
	return -1
}
