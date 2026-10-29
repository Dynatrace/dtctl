package inspect

import (
	"fmt"
	"io"

	"github.com/Dynatrace/dtctl/pkg/output"
)

// runFilter streams every record in the file through the compiled --jq program
// and collects the objects it emits (PrimFilter). This is full-file predicate
// filtering — the "give me only the matching rows" capability that row access
// alone could not provide — expressed with jq rather than a bespoke dtctl
// predicate language (D16).
//
// The output is bounded one of two ways, and both matter because a full-file
// filter can match unboundedly many rows:
//   - A row-access window carried in req.Primitive (PrimHead/PrimTail/PrimPage)
//     bounds the *matched* output here, and --head/--page additionally stop the
//     scan early once enough matches are collected.
//   - The unbounded whole-file form (PrimFilter, no window) collects every match;
//     the command layer's re-spill guard (maybeRespill / IN8) is what keeps that
//     from becoming the context blow-up the spill feature exists to prevent.
//
// jq runs per record (like `jq` over an NDJSON file), so the program is a
// predicate/transform over a single object — e.g. `select(.status == 500)` or
// `{host, timestamp}` — not an array program. Only objects are collected: a
// program that emits a scalar/array has no place in the record-oriented pipeline
// (re-spill, stats, envelope all assume records), so that is a typed usage error
// pointing the caller at an object-producing form.
func runFilter(r Reader, req Request, res *Result) error {
	prog, err := output.CompileJQ(req.Filter)
	if err != nil {
		return errBadFlags(err.Error(),
			"give --jq a valid jq program, e.g. --jq 'select(.status == 500)'")
	}

	c := newFilterCollector(req)
	for {
		rec, rerr := r.Next()
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return wrapReadError(rerr)
		}

		emitted, jerr := prog.RunRecord(rec)
		if jerr != nil {
			return errBadFlags(jerr.Error(),
				"the --jq program failed on a record; check it against the file's schema (dtctl inspect "+req.Path+" --schema)")
		}
		for _, v := range emitted {
			obj, ok := v.(map[string]interface{})
			if !ok {
				return errFilterNonObject(v)
			}
			if c.add(project(obj, req.Fields)) {
				// The window is satisfied and the rest of the file cannot change the
				// answer (head/page): stop scanning early.
				res.Kind = output.KindRecords
				res.Records = c.rows()
				return nil
			}
		}
	}

	res.Kind = output.KindRecords
	res.Records = c.rows()
	return nil
}

// filterCollector bounds the rows a streaming filter keeps, mirroring the
// row-access primitives so a windowed filter has the same shape as a windowed
// read: PrimHead keeps the first N, PrimPage keeps [offset, offset+limit),
// PrimTail keeps the last N (ring buffer), and PrimFilter keeps everything
// (the re-spill guard, not this collector, bounds that form).
type filterCollector struct {
	prim   Primitive
	out    []map[string]interface{}
	skip   int // remaining rows to skip before collecting (page offset)
	limit  int // max rows to collect; <0 means unbounded
	tailN  int // ring size for PrimTail
	ring   []map[string]interface{}
	tailCt int
}

func newFilterCollector(req Request) *filterCollector {
	c := &filterCollector{prim: req.Primitive, limit: -1}
	switch req.Primitive {
	case PrimHead:
		c.limit = rowCount(req.N)
	case PrimPage:
		if req.Offset > 0 {
			c.skip = req.Offset
		}
		c.limit = pageLimit(req.Limit)
	case PrimTail:
		c.tailN = rowCount(req.N)
	}
	if c.limit >= 0 {
		c.out = make([]map[string]interface{}, 0, prealloc(c.limit))
	}
	return c
}

// add records one matched row and reports whether the bounded window is now
// complete (so the caller can stop scanning). It never reports completion for
// the tail or unbounded forms, which must read to EOF.
func (c *filterCollector) add(row map[string]interface{}) (done bool) {
	if c.prim == PrimTail {
		if c.tailN <= 0 {
			return false
		}
		if len(c.ring) < c.tailN {
			c.ring = append(c.ring, row)
		} else {
			c.ring[c.tailCt%c.tailN] = row
		}
		c.tailCt++
		return false
	}

	if c.skip > 0 {
		c.skip--
		return false
	}
	if c.limit >= 0 && len(c.out) >= c.limit {
		return true // already full (e.g. --head 0 / --limit 0)
	}
	c.out = append(c.out, row)
	return c.limit >= 0 && len(c.out) >= c.limit
}

// rows returns the collected rows, materialising the tail ring in arrival order.
func (c *filterCollector) rows() []map[string]interface{} {
	if c.prim != PrimTail {
		if c.out == nil {
			return []map[string]interface{}{}
		}
		return c.out
	}
	size := c.tailN
	if c.tailCt < size {
		size = c.tailCt
	}
	out := make([]map[string]interface{}, 0, size)
	start := c.tailCt - size
	for i := start; i < c.tailCt; i++ {
		out = append(out, c.ring[i%c.tailN])
	}
	return out
}

// errFilterNonObject reports a --jq program that emitted a non-object value. The
// record-oriented pipeline (re-spill, stats, the kind:records envelope) only
// carries objects, so a scalar/array output is a usage error rather than a
// silently dropped or mangled row.
func errFilterNonObject(v interface{}) *Error {
	return errBadFlags(
		fmt.Sprintf("--jq must emit objects (records), but it emitted a %s", jsonTypeName(v)),
		"wrap the projection in an object, e.g. --jq 'select(.status == 500)' or --jq '{host, timestamp}'",
		"for free-form extraction into scalars, run jq over the spilled file directly with your own tooling",
	)
}

// jsonTypeName names the JSON kind of a value emitted by gojq, for an actionable
// "filter emitted a <type>" message.
func jsonTypeName(v interface{}) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case int, float64:
		return "number"
	case string:
		return "string"
	case []interface{}:
		return "array"
	default:
		return fmt.Sprintf("%T", v)
	}
}
