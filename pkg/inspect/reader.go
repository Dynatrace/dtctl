package inspect

import (
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/Dynatrace/dtctl/pkg/output"
)

// Reader streams records out of a spilled file one at a time, so a row-access
// primitive reads only what it returns and memory never scales with the file
// size. Row order is file order (= spill order = the DQL result order); a reader
// never re-sorts (sorting is a query concern — push `| sort` into DQL, IN7).
type Reader interface {
	// Columns returns the column names when the format carries a cheap schema
	// (Parquet footer, CSV header). It returns nil when the schema is only
	// knowable by scanning rows (NDJSON / JSON), in which case the caller falls
	// back to the sidecar schema or best-effort discovery.
	Columns() []string
	// Next returns the next record, or io.EOF when the stream is exhausted. The
	// returned map is owned by the caller.
	Next() (map[string]interface{}, error)
	// Close releases the underlying file/handles.
	Close() error
}

// openReader opens path with the reader for the given format. format is the
// authoritative format from the sidecar when present, else inferred from the
// extension by the caller. A missing file is reported as a not-found error and a
// reader that cannot initialise (truncated/corrupt header) as unreadable, both
// carrying the original query for a concrete re-query suggestion.
func openReader(path, format, query string) (Reader, error) {
	f, err := os.Open(path) //nolint:gosec // path is a user/agent-provided spill file by design
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errNotFound(path, query, err)
		}
		return nil, errUnreadable(path, query, err)
	}

	switch strings.ToLower(format) {
	case "jsonl", "ndjson":
		return newNDJSONReader(f, path, query), nil
	case "json":
		r, rerr := newJSONReader(f, path, query)
		if rerr != nil {
			_ = f.Close()
			return nil, rerr
		}
		return r, nil
	case "csv":
		r, rerr := newCSVReader(f, path, query)
		if rerr != nil {
			_ = f.Close()
			return nil, rerr
		}
		return r, nil
	case "parquet":
		r, rerr := newParquetReader(f, path, query)
		if rerr != nil {
			_ = f.Close()
			return nil, rerr
		}
		return r, nil
	default:
		_ = f.Close()
		return nil, errBadFlags("unsupported file format " + quote(format) + " (inspect reads jsonl, json, csv, or parquet)")
	}
}

// formatFromExtension infers a spill format from a file extension. It is the
// fallback when there is no sidecar to declare the format authoritatively. It
// delegates to the shared output.FormatForExt so the extension→format mapping
// lives in one place.
func formatFromExtension(path string) string {
	return output.FormatForExt(path)
}

// project returns a copy of rec limited to fields, preserving the requested
// order semantics at the map level (maps are unordered; the printer/encoder
// decides column order). A field absent from rec is simply omitted (it reads as
// null downstream), matching DQL's `fields` behaviour. fields == nil returns rec
// unchanged (no projection requested).
func project(rec map[string]interface{}, fields []string) map[string]interface{} {
	if len(fields) == 0 {
		return rec
	}
	out := make(map[string]interface{}, len(fields))
	for _, f := range fields {
		if v, ok := rec[f]; ok {
			out[f] = v
		}
	}
	return out
}

// --- small shared string helpers -------------------------------------------

func quote(s string) string { return strconv.Quote(s) }

func joinColumns(cols []string) string {
	if len(cols) == 0 {
		return "(none)"
	}
	sorted := make([]string, len(cols))
	copy(sorted, cols)
	sort.Strings(sorted)
	return strings.Join(sorted, ", ")
}
