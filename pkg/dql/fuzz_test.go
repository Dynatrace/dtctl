package dql

import (
	"strings"
	"testing"
)

// FuzzInsertFilter holds the rewrite to what makes it safe to apply by
// default, for any text at all:
//
//   - it does not panic;
//   - a refusal returns the input byte for byte, with a reason;
//   - an applied rewrite is the input plus one run of bytes, the stage
//     "| filter (<expr>)", at the first depth-0 pipe or at the end of the text;
//   - the result scans with that stage as its second depth-0 stage, and every
//     byte of the input reads as it did before: nothing outside the inserted
//     stage changed from code to text or moved to another bracket depth;
//   - applying the rewrite to its own result inserts nothing: the query now
//     filters a bound field itself, which is the user_filter refusal.
func FuzzInsertFilter(f *testing.F) {
	for _, tt := range insertFilterTests {
		f.Add(tt.query)
	}
	f.Fuzz(func(t *testing.T, query string) {
		got := InsertFilter(query, testFilters)
		if got.Outcome != Applied {
			if got.Query != query {
				t.Fatalf("%s changed the query: %q became %q", got.Outcome, query, got.Query)
			}
			if got.Reason == "" || got.Filter != "" {
				t.Fatalf("%s without a reason, or with a filter: %+v", got.Outcome, got)
			}
			return
		}

		in, ok := Scan(query)
		if !ok {
			t.Fatalf("applied to text the scan refuses: %q", query)
		}
		stage := "| filter (" + testFilters[got.DataObject].Expr + ")"
		at, inserted := in.Segments[0].End, stage+"\n"
		if len(in.Segments) == 1 {
			at, inserted = len(query), "\n"+stage
		}
		if want := query[:at] + inserted + query[at:]; got.Query != want {
			t.Fatalf("not the input plus one stage at %d:\n input %q\n   got %q\n  want %q", at, query, got.Query, want)
		}

		out, ok := Scan(got.Query)
		if !ok {
			t.Fatalf("the result does not scan: %q", got.Query)
		}
		if len(out.Segments) != len(in.Segments)+1 {
			t.Fatalf("%d stages became %d: %q", len(in.Segments), len(out.Segments), got.Query)
		}
		second := out.Segments[1]
		if text := strings.TrimSpace(got.Query[second.Start:second.End]); second.Command != "filter" || "| "+text != stage {
			t.Fatalf("the second stage is %q, not the inserted filter: %q", text, got.Query)
		}
		after := at + len(inserted)
		if out.Code[:at] != in.Code[:at] || out.Code[after:] != in.Code[at:] {
			t.Fatalf("the input reads differently around the inserted stage:\n before %q\n  after %q", in.Code, out.Code)
		}
		for i := range in.Depth {
			j := i
			if i >= at {
				j += len(inserted)
			}
			if in.Depth[i] != out.Depth[j] {
				t.Fatalf("byte %d moved from depth %d to %d: %q", i, in.Depth[i], out.Depth[j], got.Query)
			}
		}

		again := InsertFilter(got.Query, testFilters)
		if again.Outcome != UserFilter || again.Query != got.Query {
			t.Fatalf("a second pass was %s and sent %q", again.Outcome, again.Query)
		}
	})
}

// FuzzScan holds the masked view to its contract: Code and Depth have the
// query's length, a byte is either kept or masked, and the stages tile the
// text, split at pipes the scan left as code at depth 0.
func FuzzScan(f *testing.F) {
	for _, tt := range insertFilterTests {
		f.Add(tt.query)
	}
	f.Fuzz(func(t *testing.T, query string) {
		m, ok := Scan(query)
		if !ok {
			return
		}
		if len(m.Code) != len(query) || len(m.Depth) != len(query) {
			t.Fatalf("lengths differ: query %d, code %d, depth %d", len(query), len(m.Code), len(m.Depth))
		}
		for i := range len(query) {
			if m.Code[i] != query[i] && m.Code[i] != mask {
				t.Fatalf("byte %d of %q became %q", i, query, m.Code[i])
			}
			if m.Depth[i] < 0 {
				t.Fatalf("byte %d of %q has depth %d", i, query, m.Depth[i])
			}
		}
		if len(m.Segments) == 0 || m.Segments[0].Start != 0 || m.Segments[len(m.Segments)-1].End != len(query) {
			t.Fatalf("the stages do not span %q: %+v", query, m.Segments)
		}
		for i, seg := range m.Segments[1:] {
			pipe := m.Segments[i].End
			if seg.Start != pipe+1 || m.Code[pipe] != '|' || m.Depth[pipe] != 0 {
				t.Fatalf("stage %d of %q does not start after a depth-0 pipe: %+v", i+1, query, m.Segments)
			}
		}
	})
}

// FuzzUngroupedCount: no panic, and a column only for text that scans.
func FuzzUngroupedCount(f *testing.F) {
	for _, q := range []string{
		"fetch logs | summarize c = count()",
		"fetch logs | summarize {total = count(), slow = countIf(duration > 1s)}",
		"fetch logs\n| summarize c = count() // every record\n",
		"fetch logs | summarize c = count(), by:{host}",
		"fetch logs | summarize `my count` = count()",
	} {
		f.Add(q)
	}
	f.Fuzz(func(t *testing.T, query string) {
		column, ok := UngroupedCount(query)
		if ok != (column != "") {
			t.Fatalf("ok is %v with column %q for %q", ok, column, query)
		}
		if _, scans := Scan(query); ok && !scans {
			t.Fatalf("a column for text the scan refuses: %q", query)
		}
	})
}

// unquote reads a "…" string the way DQL does for the escapes Quote writes.
// ok is false for anything else: a missing quote, an unescaped quote inside,
// or an escape Quote never produces.
func unquote(literal string) (value string, ok bool) {
	if len(literal) < 2 || literal[0] != '"' || literal[len(literal)-1] != '"' {
		return "", false
	}
	escapes := map[byte]byte{'\\': '\\', '"': '"', 'n': '\n', 'r': '\r', 't': '\t'}
	var b strings.Builder
	for body := literal[1 : len(literal)-1]; body != ""; body = body[1:] {
		switch body[0] {
		case '"':
			return "", false
		case '\\':
			if len(body) < 2 || escapes[body[1]] == 0 {
				return "", false
			}
			b.WriteByte(escapes[body[1]])
			body = body[1:]
		default:
			b.WriteByte(body[0])
		}
	}
	return b.String(), true
}

// FuzzQuote: whatever the value, the literal is one string on one line, and
// reading it back gives the value.
func FuzzQuote(f *testing.F) {
	for _, v := range []string{"checkout", `say "hi"`, `C:\path`, "line\nbreak", `\"`, `""`, "✓"} {
		f.Add(v)
	}
	f.Fuzz(func(t *testing.T, v string) {
		got := Quote(v)
		m, ok := Scan(got)
		if !ok || m.Code != `"`+strings.Repeat(string(mask), len(got)-2)+`"` {
			t.Fatalf("Quote(%q) = %q is not one string literal", v, got)
		}
		if strings.ContainsAny(got, "\n\r") {
			t.Fatalf("Quote(%q) = %q spans lines", v, got)
		}
		if back, ok := unquote(got); !ok || back != v {
			t.Fatalf("Quote(%q) = %q reads back as %q (ok %v)", v, got, back, ok)
		}
	})
}
