package cmd

import (
	"github.com/dynatrace-oss/dtctl/pkg/output"
	"github.com/dynatrace-oss/dtctl/pkg/resources/analyzer"
)

// shapeEffect reports what shapeAnalyzerForAgent changed, so the caller can name the
// flags that restore the raw result.
type shapeEffect struct {
	output.SeriesEffect
	NoFindings bool // the run succeeded and its output is empty
}

// shapeAnalyzerForAgent reduces an analyzer result for a model reader: the echoed
// input and the DQL `types` blocks go, nulls go, and every embedded DQL result
// (`{metadata, records, types}`) gets --series/--precision applied to its
// records. Returns a new value; the input is not modified.
func shapeAnalyzerForAgent(r *analyzer.ExecuteResult, mode output.SeriesMode, digits int) (map[string]interface{}, shapeEffect) {
	var eff shapeEffect
	out := map[string]interface{}{}
	if r.RequestToken != "" {
		out["requestToken"] = r.RequestToken
	}
	if r.TTLInSeconds != 0 {
		out["ttlInSeconds"] = r.TTLInSeconds
	}
	if r.Result == nil {
		return out, eff
	}
	res := map[string]interface{}{
		"resultId":        r.Result.ResultID,
		"resultStatus":    r.Result.ResultStatus,
		"executionStatus": r.Result.ExecutionStatus,
	}
	if len(r.Result.Output) > 0 {
		res["output"] = shapeValue(toAny(r.Result.Output), mode, digits, &eff.SeriesEffect)
	} else if r.Result.ResultStatus == "SUCCESSFUL" {
		eff.NoFindings = true
	}
	if len(r.Result.Data) > 0 {
		res["data"] = shapeValue(toAny(r.Result.Data), mode, digits, &eff.SeriesEffect)
	}
	if len(r.Result.Logs) > 0 {
		res["logs"] = r.Result.Logs
	}
	out["result"] = res
	return out, eff
}

func toAny(in []map[string]interface{}) []interface{} {
	out := make([]interface{}, len(in))
	for i, m := range in {
		out[i] = m
	}
	return out
}

// shapeValue walks the tree, dropping nulls and `types` keys and shaping the
// records of any embedded DQL result.
func shapeValue(v interface{}, mode output.SeriesMode, digits int, eff *output.SeriesEffect) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		if recs, ok := dqlRecords(t); ok {
			shaped, e := output.ApplySeriesModeWithEffect(recs, mode, digits, false)
			eff.Summarized = eff.Summarized || e.Summarized
			eff.Rounded = eff.Rounded || e.Rounded
			out := make(map[string]interface{}, len(t))
			for k, val := range t {
				switch k {
				case "types":
				case "records":
					out[k] = dropNulls(toAny(shaped))
				default:
					out[k] = shapeValue(val, mode, digits, eff)
				}
			}
			return out
		}
		out := make(map[string]interface{}, len(t))
		for k, val := range t {
			if val == nil {
				continue
			}
			out[k] = shapeValue(val, mode, digits, eff)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, val := range t {
			out[i] = shapeValue(val, mode, digits, eff)
		}
		return out
	default:
		return v
	}
}

// dqlRecords recognizes an embedded DQL result: a `records` list of objects
// next to a `types` or `metadata` key.
func dqlRecords(m map[string]interface{}) ([]map[string]interface{}, bool) {
	raw, ok := m["records"].([]interface{})
	if !ok {
		return nil, false
	}
	if _, hasTypes := m["types"]; !hasTypes {
		if _, hasMeta := m["metadata"]; !hasMeta {
			return nil, false
		}
	}
	recs := make([]map[string]interface{}, 0, len(raw))
	for _, r := range raw {
		rec, ok := r.(map[string]interface{})
		if !ok {
			return nil, false
		}
		recs = append(recs, rec)
	}
	return recs, true
}

// dropNulls removes nil fields from record objects (not from series arrays,
// whose nulls are gaps with positional meaning).
func dropNulls(recs []interface{}) []interface{} {
	for i, r := range recs {
		rec, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		out := make(map[string]interface{}, len(rec))
		for k, v := range rec {
			if v != nil {
				out[k] = v
			}
		}
		recs[i] = out
	}
	return recs
}
