package cmd

import (
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dynatrace-oss/dtctl/pkg/config"
	"github.com/dynatrace-oss/dtctl/pkg/exec"
	"github.com/dynatrace-oss/dtctl/pkg/inspect"
	"github.com/dynatrace-oss/dtctl/pkg/output"
)

// maybeRespill implements INSPECT IN8: a row-access result that is itself a
// context hazard (e.g. --head 1000000, or a wide projection over many rows)
// re-spills to a NEW managed file and returns a result-file manifest, exactly
// like `query` does — inspect must not become the very context blowup the spill
// feature exists to prevent. It reuses the Layer 1 spill machinery (dir
// resolution, atomic write, stats, sidecar, TTL prune); only the provenance is
// inspect-specific (derived from the source file's sidecar, not a Grail query).
//
// It returns (nil, nil) when the result should be emitted inline (spill disabled,
// or under threshold in auto mode). A spill of a spill is fine — it just
// re-partitions under the active context dir.
func maybeRespill(cmd *cobra.Command, cfg *config.Config, req inspect.Request, res *inspect.Result) (*output.Response, error) {
	opts, err := resolveSpillOptions(cmd, cfg)
	if err != nil {
		return nil, err
	}
	if !opts.Enabled() {
		return nil, nil
	}

	// Only reached in agent mode, so the rows would go out as result.records of
	// an envelope: measure them in the layout EncodeEnvelope gives stdout (#570).
	measured, encoding := output.MeasureSerializedBytes(res.Records, outputFormat(cmdContext(cmd)), output.EnvelopeRecordsLayout(currentStdout(cmdContext(cmd))))
	if opts.Mode == exec.SpillAuto && measured <= opts.Threshold {
		return nil, nil // inline: small enough
	}

	// Provenance: carry the original sampling/tenant forward from the source
	// sidecar; partition the new file under the active context.
	var sampled bool
	var samplingRatio float64
	tenantID := req.ActiveTenant
	if res.Sidecar != nil {
		sampled = res.Sidecar.Sampled
		samplingRatio = res.Sidecar.SamplingRatio
		if tenantID == "" {
			tenantID = res.Sidecar.TenantID
		}
	}
	synthQuery := describeInspectCall(req)

	format, targetPath, baseDir, managed, summaryOnly, warnings := resolveRespillTarget(opts, req.ActiveContext, synthQuery)

	records := res.Records
	cols := output.ComputeColumnStats(records, sampled, output.DefaultStatsTopK, output.DefaultStatsMaxDistinct)
	envCols, omitted := output.CapColumnsForEnvelope(cols, output.DefaultMaxSummaryColumns)

	manifest := &output.ResultFileManifest{
		Query:         synthQuery,
		Format:        format,
		Rows:          len(records),
		ContextName:   req.ActiveContext,
		TenantID:      tenantID,
		Sampled:       sampled,
		SamplingRatio: samplingRatio,
		SampleRows:    output.SampleRows(records, output.DefaultSampleRows),
	}
	manifest.SetStats(envCols, sampled)
	manifest.ColumnsOmitted = omitted

	decided := "spilled"
	if !summaryOnly {
		written, werr := output.WriteSpillFile(targetPath, func(w io.Writer) error {
			p := newPrinterOpts(cmdContext(cmd), output.PrinterOptions{
				Format: format,
				Writer: w,
				Types:  respillTypes(res.Sidecar),
			})
			return p.PrintList(records)
		})
		if werr != nil {
			if opts.ToPath != "" {
				return nil, fmt.Errorf("failed to write spill file %q: %w", targetPath, werr)
			}
			summaryOnly = true
			warnings = append(warnings, "spill write failed; returning overview only")
		} else {
			manifest.Kind = output.KindResultFile
			manifest.Path = targetPath
			manifest.Bytes = written
			_ = output.WriteSidecar(targetPath, &output.SidecarManifest{
				EnvelopeVersion: output.EnvelopeVersion,
				Format:          format,
				Sampled:         sampled,
				SamplingRatio:   samplingRatio,
				TenantID:        tenantID,
				ContextName:     req.ActiveContext,
				Query:           synthQuery,
				Rows:            len(records),
				Bytes:           written,
				Created:         time.Now().UTC(),
				Columns:         cols,
			})
			if managed && baseDir != "" {
				output.PruneOldSpills(baseDir, opts.TTL)
			}
		}
	}

	if summaryOnly {
		manifest.Kind = output.KindSummaryOnly
		decided = "summary-only"
	}

	// Carry forward any engine warnings (e.g. sampling-unknown) onto the spill.
	warnings = append(res.Warnings, warnings...)

	// Note when the re-spilled file is the OUTPUT of a --jq filter (the engine
	// applied jq per record over the whole source file, IN-filter), so a reader
	// knows the file already holds the filtered/transformed rows — not the source.
	if req.Filter != "" {
		warnings = append(warnings, "this file holds the rows matched by --jq, not the full source result")
	}

	suggestions := []string{}
	if manifest.Kind == output.KindResultFile {
		suggestions = append(suggestions,
			"# the inspected rows were large, so they were re-spilled to the path above; read it with your file tooling for row-level follow-up",
			"# for complex local analysis, process the spilled file with your preferred local analytics tooling")
	} else {
		suggestions = append(suggestions,
			"# the inspected rows were large but no writable spill location was available, so only this overview is returned",
			"# narrow the request (smaller --head/--limit, or --fields) to get rows inline")
	}

	total := len(records)
	resp := &output.Response{
		OK:              true,
		EnvelopeVersion: output.EnvelopeVersion,
		Result:          manifest,
		Context: &output.ResponseContext{
			Verb:             "inspect",
			Resource:         inspectResourceFromSidecar(res.Sidecar),
			Total:            &total,
			Decided:          decided,
			ThresholdBytes:   opts.Threshold,
			MeasuredBytes:    measured,
			MeasuredEncoding: encoding,
			Warnings:         warnings,
			Suggestions:      suggestions,
		},
	}
	return resp, nil
}

// resolveRespillTarget picks the format, destination, and base dir for a re-spill,
// degrading to summary-only when there is no writable location (D8). It mirrors
// the query path's resolution but is kept local so the query spill path stays
// untouched.
func resolveRespillTarget(opts exec.SpillOptions, contextName, synthQuery string) (format, targetPath, baseDir string, managed, summaryOnly bool, warnings []string) {
	if opts.ToPath != "" {
		format = respillFormatForPath(opts.ToPath, opts.Format)
		return format, opts.ToPath, "", false, false, []string{
			"spill path is a user-chosen location and opts out of the managed privacy guarantees (no TTL pruning, no per-context partitioning); you own its lifetime",
		}
	}

	format = strings.ToLower(opts.Format)
	if format == "" {
		format = "jsonl"
	}

	base, isManaged, berr := output.SpillBaseDir(opts.Dir)
	if berr != nil {
		return format, "", "", false, true, []string{"no writable spill location — overview only; narrow the request to get rows inline"}
	}
	dir := base
	if isManaged {
		dir = filepath.Join(base, output.SanitizeContextName(contextName))
	} else {
		warnings = append(warnings, "spill dir is a user-chosen location and opts out of the managed privacy guarantees")
	}
	hash := output.SpillHash(synthQuery)
	targetPath = filepath.Join(dir, "q-"+hash+"."+respillExt(format))
	return format, targetPath, base, isManaged, false, warnings
}

// respillTypes builds the Parquet column-type mapping from the source sidecar's
// column profile so a Parquet re-spill carries a faithful columnar schema. nil
// (no sidecar) lets the Parquet writer fall back to value inference.
func respillTypes(sc *output.SidecarManifest) []output.ColumnTypeMapping {
	if sc == nil || len(sc.Columns) == 0 {
		return nil
	}
	out := make([]output.ColumnTypeMapping, 0, len(sc.Columns))
	for _, c := range sc.Columns {
		out = append(out, output.ColumnTypeMapping{Name: c.Name, Type: c.Type})
	}
	return out
}

// describeInspectCall renders a stable, human-legible description of the inspect
// invocation, recorded as the re-spilled file's `query` provenance (so a later
// stale-file recovery suggests re-running the same inspect) and hashed into its
// filename (so re-running overwrites in place rather than accumulating).
func describeInspectCall(req inspect.Request) string {
	var b strings.Builder
	b.WriteString("inspect ")
	b.WriteString(req.Path)
	// A --jq filter (PrimFilter, or any primitive with req.Filter set) carries the
	// window as a bound, not a primitive, so record both the filter and the bound.
	if req.Filter != "" {
		fmt.Fprintf(&b, " --jq %s", strconv.Quote(req.Filter))
	}
	switch req.Primitive {
	case inspect.PrimHead:
		fmt.Fprintf(&b, " --head %d", req.N)
	case inspect.PrimTail:
		fmt.Fprintf(&b, " --tail %d", req.N)
	case inspect.PrimPage:
		fmt.Fprintf(&b, " --page --offset %d --limit %d", req.Offset, req.Limit)
	}
	if len(req.Fields) > 0 {
		fmt.Fprintf(&b, " --fields %s", strings.Join(req.Fields, ","))
	}
	return b.String()
}

func respillExt(format string) string {
	return output.ExtForFormat(format)
}

func respillFormatForPath(path, fallback string) string {
	if ext := spillFormatFromExt(path); ext != "" {
		return ext
	}
	if fallback != "" {
		return strings.ToLower(fallback)
	}
	return "jsonl"
}
