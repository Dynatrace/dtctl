package dql

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScan_MasksLiteralsAndComments(t *testing.T) {
	tests := []struct {
		name         string
		query        string
		code         string
		commands     []string
		comments     bool
		singleQuotes bool
	}{
		{
			name:     "plain pipeline",
			query:    "fetch logs | limit 5",
			code:     "fetch logs | limit 5",
			commands: []string{"fetch", "limit"},
		},
		{
			name:     "a pipe inside a string is text",
			query:    `fetch logs | filter content == "a|b"`,
			code:     `fetch logs | filter content == "###"`,
			commands: []string{"fetch", "filter"},
		},
		{
			name:     "an escaped quote does not end the string",
			query:    `fetch logs | filter content == "a\"|b"`,
			code:     `fetch logs | filter content == "#####"`,
			commands: []string{"fetch", "filter"},
		},
		{
			name:     "a triple-quoted string holds plain quotes",
			query:    `fetch logs | filter content == """a "|" b"""`,
			code:     `fetch logs | filter content == """#######"""`,
			commands: []string{"fetch", "filter"},
		},
		{
			name:     "a backtick identifier is masked",
			query:    "fetch logs | fields `a|b`",
			code:     "fetch logs | fields `###`",
			commands: []string{"fetch", "fields"},
		},
		{
			name:     "an escaped backtick does not end the identifier",
			query:    "fetch logs | fields `a\\`|b`, `c\\\\`",
			code:     "fetch logs | fields `#####`, `###`",
			commands: []string{"fetch", "fields"},
		},
		{
			name:     "a line comment runs to the end of the line",
			query:    "fetch logs // a | b\n| limit 1",
			code:     "fetch logs ########\n| limit 1",
			commands: []string{"fetch", "limit"},
			comments: true,
		},
		{
			name:     "a block comment is masked with its delimiters",
			query:    "fetch logs /* | */ | limit 1",
			code:     "fetch logs ####### | limit 1",
			commands: []string{"fetch", "limit"},
			comments: true,
		},
		{
			name:     "a comment before a command",
			query:    "fetch logs | /* c */ filter x",
			code:     "fetch logs | ####### filter x",
			commands: []string{"fetch", "filter"},
			comments: true,
		},
		{
			name:     "a pipe inside brackets does not split the pipeline",
			query:    "fetch logs | filter in(x, [fetch spans | fields id])",
			code:     "fetch logs | filter in(x, [fetch spans | fields id])",
			commands: []string{"fetch", "filter"},
		},
		{
			name:         "a single quote is reported, not masked",
			query:        "fetch logs | filter a == 'x'",
			code:         "fetch logs | filter a == 'x'",
			commands:     []string{"fetch", "filter"},
			singleQuotes: true,
		},
		{
			name:     "a single quote inside a string or comment is not reported",
			query:    "fetch logs | filter a == \"it's\" // it's",
			code:     "fetch logs | filter a == \"####\" #######",
			commands: []string{"fetch", "filter"},
			comments: true,
		},
		{
			name:     "a stage without a command word",
			query:    "fetch logs | ",
			code:     "fetch logs | ",
			commands: []string{"fetch", ""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, ok := Scan(tt.query)
			require.True(t, ok)
			assert.Equal(t, tt.code, s.Code)
			assert.Len(t, s.Depth, len(tt.query), "Depth has one entry per byte")
			assert.Equal(t, tt.comments, s.HasComments)
			assert.Equal(t, tt.singleQuotes, s.singleQuote)

			var commands []string
			for _, seg := range s.Segments {
				commands = append(commands, seg.Command)
			}
			assert.Equal(t, tt.commands, commands)
		})
	}
}

func TestScan_Segments(t *testing.T) {
	q := "fetch logs | filter x == 1|limit 5"
	s, ok := Scan(q)
	require.True(t, ok)
	require.Len(t, s.Segments, 3)
	assert.Equal(t, "fetch logs ", q[s.Segments[0].Start:s.Segments[0].End])
	assert.Equal(t, " filter x == 1", q[s.Segments[1].Start:s.Segments[1].End])
	assert.Equal(t, "limit 5", q[s.Segments[2].Start:s.Segments[2].End])
}

func TestScan_Depth(t *testing.T) {
	q := `a(b[c]{"(d)"})`
	s, ok := Scan(q)
	require.True(t, ok)
	// A bracket belongs to its outer level; a literal's bytes share the
	// depth of the brackets around it, whatever they contain.
	assert.Equal(t, []int{0, 0, 1, 1, 2, 1, 1, 2, 2, 2, 2, 2, 1, 0}, s.Depth)
}

func TestScan_Refuses(t *testing.T) {
	for _, q := range []string{
		`fetch logs | filter content == "unterminated`,
		`fetch logs | filter content == """unterminated`,
		"fetch logs | fields `unterminated",
		"fetch logs /* unterminated",
		"fetch logs | filter (a == 1",
		"fetch logs | filter a == 1)",
		"fetch logs | filter in(a, [1, 2)]",
		`fetch logs | filter content == "ends in a backslash\`,
		"fetch logs | fields `ends in an escaped backtick\\`",
	} {
		t.Run(q, func(t *testing.T) {
			_, ok := Scan(q)
			assert.False(t, ok)
		})
	}
}

func TestQuote(t *testing.T) {
	tests := map[string]string{
		"checkout":       `"checkout"`,
		`say "hi"`:       `"say \"hi\""`,
		`C:\path`:        `"C:\\path"`,
		`\"`:             `"\\\""`,
		"":               `""`,
		"unicode ✓ name": `"unicode ✓ name"`,
	}
	for in, want := range tests {
		got := Quote(in)
		assert.Equal(t, want, got, in)
		// What it produces scans as one complete literal.
		s, ok := Scan(got)
		require.True(t, ok, got)
		assert.Equal(t, `"`+strings.Repeat("#", len(got)-2)+`"`, s.Code)
	}
}
