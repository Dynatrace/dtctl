package exec

import (
	"io"
	"time"

	"github.com/Dynatrace/dtctl/pkg/output"
)

// spillProvenanceOf reads the provenance a spill records from the Grail
// metadata: whether the result was sampled (and at what ratio), the canonical
// query and the analysis timeframe that key the spill filename (D10).
func spillProvenanceOf(query string, result *DQLQueryResponse, opts DQLExecuteOptions) (sampled bool, canonical, tfStart, tfEnd string, samplingRatio float64) {
	canonical = query
	if g := result.GetMetadata(); g != nil {
		sampled = g.Sampled
		if g.CanonicalQuery != "" {
			canonical = g.CanonicalQuery
		}
		if g.AnalysisTimeframe != nil {
			tfStart, tfEnd = g.AnalysisTimeframe.Start, g.AnalysisTimeframe.End
		}
	}
	if sampled {
		samplingRatio = opts.DefaultSamplingRatio
	}
	return sampled, canonical, tfStart, tfEnd, samplingRatio
}

// writeResultFile writes the full rows to targetPath atomically, then the
// sidecar manifest (D34), then prunes the managed cache (D11). It returns the
// data file's size. Only the data write can fail it: the sidecar and the prune
// are best-effort and must not fail the query.
func writeResultFile(targetPath, format, query string, result *DQLQueryResponse, records []map[string]interface{}, cols []output.ColumnStats, sampled bool, samplingRatio float64, managed bool, baseDir string, opts DQLExecuteOptions) (int64, error) {
	written, err := output.WriteSpillFile(targetPath, func(w io.Writer) error {
		// Types is only consumed by the Parquet writer (to build a faithful
		// columnar schema from the DQL column types); the json/jsonl/csv writers
		// ignore it. It is nil unless --include-types was requested, which the
		// command layer auto-enables for a Parquet spill — otherwise the Parquet
		// writer falls back to value inference.
		p := output.NewPrinterWithOpts(output.PrinterOptions{
			Format: format,
			Writer: w,
			Types:  columnTypeMappings(result),
		})
		return p.PrintList(records)
	})
	if err != nil {
		return 0, err
	}

	// Written last so its presence implies a complete data file.
	_ = output.WriteSidecar(targetPath, &output.SidecarManifest{
		EnvelopeVersion: output.EnvelopeVersion,
		Format:          format,
		Sampled:         sampled,
		SamplingRatio:   samplingRatio,
		TenantID:        opts.TenantID,
		ContextName:     opts.ContextName,
		Query:           query,
		Rows:            len(records),
		Bytes:           written,
		Created:         time.Now().UTC(),
		Columns:         cols,
	})

	if managed && baseDir != "" {
		output.PruneOldSpills(baseDir, opts.Spill.TTL)
	}
	return written, nil
}
