package exec

import (
	"fmt"
	"time"

	"github.com/dynatrace-oss/dtctl/pkg/output"
)

// finishStreamedJSONL closes out a `-o jsonl` stream: the rows are already on
// stdout, so only the approximations and notifications are left for stderr.
// The buffered path prints them ahead of the rows; a stream only learns them
// from the response metadata, which follows the rows. Like the buffered jsonl
// branch it prints no metadata footer.
func (e *DQLExecutor) finishStreamedJSONL(query string, result *DQLQueryResponse) error {
	for _, w := range approximationWarnings(result) {
		output.FprintWarning(e.errW(), "%s", w)
	}
	notifications := result.GetNotifications()
	if len(notifications) > 0 {
		e.PrintNotifications(notifications)
		if advice := unsortedSummarizeAdvice(query, notifications); advice != "" {
			output.FprintHint(e.errW(), "%s", advice)
		}
	}
	return nil
}

// finishStreamedSpill commits the streamed file and emits the summary envelope.
//
// It is buildSpillResponse for a result that was never held: the column stats,
// the constant/all-null columns and the sample rows come from the accumulators
// the collector folded each row into, and the row count from its counter.
// Nothing below re-reads the rows.
func (e *DQLExecutor) finishStreamedSpill(query string, result *DQLQueryResponse, c *streamCollector, opts DQLExecuteOptions) error {
	resp, err := e.buildStreamedSpillResponse(query, result, c, opts)
	if err != nil {
		return err
	}
	return output.EncodeEnvelope(e.outW(), resp)
}

func (e *DQLExecutor) buildStreamedSpillResponse(query string, result *DQLQueryResponse, c *streamCollector, opts DQLExecuteOptions) (output.Response, error) {
	sampled, canonical, tfStart, tfEnd, samplingRatio := spillProvenanceOf(query, result, opts)

	format, targetPath, baseDir, managed, summaryOnly, warnings, err := e.resolveSpillTarget(canonical, tfStart, tfEnd, opts)
	if err != nil {
		return output.Response{}, err
	}
	summaryReason := ""
	if summaryOnly {
		summaryReason = summaryReasonNoLocation
	}
	if !summaryOnly && c.writeFailed {
		summaryOnly = true
		summaryReason = summaryReasonWriteFailed
		warnings = append(warnings, "spill write failed; returning overview only")
	}

	cols := c.stats.Finalize(sampled)
	compaction := c.compaction()
	rows := c.rows

	// The same column view and sample rows buildSpillResponse derives from a
	// held result: the sample drops constant columns and null values alike.
	summaryCols := cols
	sampleSource := c.sample
	if compaction != nil {
		summaryCols = compaction.FilterColumns(cols)
		sampleSource = compaction.Sparse(c.sample)
	}
	sampleRows := output.SampleRows(sampleSource, output.DefaultSampleRows)
	envCols, omittedCols := output.CapColumnsForEnvelope(summaryCols, output.DefaultMaxSummaryColumns)

	manifest := &output.ResultFileManifest{
		Query:         query,
		Format:        format,
		Rows:          rows,
		ContextName:   opts.ContextName,
		TenantID:      opts.TenantID,
		Sampled:       sampled,
		SamplingRatio: samplingRatio,
		SampleRows:    sampleRows,
	}
	manifest.SetStats(envCols, sampled)
	manifest.ColumnsOmitted = omittedCols
	manifest.Types = emittedTypes(result, opts)
	if compaction != nil {
		manifest.Constant = compaction.EnvelopeConstant()
		manifest.NullColumns = compaction.NullColumns
	}

	decided := "spilled"
	if summaryOnly {
		// The rows went nowhere (no writable location, or the write failed), so
		// drop the temp file.
		c.abort()
	} else {
		written, cerr := c.writer.Commit(targetPath)
		if cerr != nil {
			if opts.Spill.ToPath != "" {
				return output.Response{}, fmt.Errorf("failed to write spill file %q: %w", targetPath, cerr)
			}
			summaryOnly = true
			summaryReason = summaryReasonWriteFailed
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
				TenantID:        opts.TenantID,
				ContextName:     opts.ContextName,
				Query:           query,
				Rows:            rows,
				Bytes:           written,
				Created:         time.Now().UTC(),
				Columns:         cols,
			})
			if managed && baseDir != "" {
				output.PruneOldSpills(baseDir, opts.Spill.TTL)
			}
		}
	}

	if summaryOnly {
		manifest.Kind = output.KindSummaryOnly
		decided = "summary-only"
	}

	// planStream never streams under --jq, so unlike buildSpillResponse there is
	// no unapplied-filter warning to add, and a streamed result is never empty.
	adviceWarnings, suggestions := spillAdvice(query, result, manifest, summaryReason, len(omittedCols), compaction, nil, opts)
	warnings = append(warnings, adviceWarnings...)

	total := rows
	ctx := &output.ResponseContext{
		Verb:           "query",
		Resource:       resourceFromQuery(query),
		Total:          &total,
		Decided:        decided,
		ThresholdBytes: opts.Spill.Threshold,
		// No measured_bytes: the rows were never serialised for inline emission.
		Streamed:    true,
		Warnings:    warnings,
		Suggestions: suggestions,
	}
	if opts.Decorate != nil {
		opts.Decorate(ctx, result, nil)
	}

	return output.Response{
		OK:              true,
		EnvelopeVersion: output.EnvelopeVersion,
		Result:          manifest,
		Context:         ctx,
		Metadata:        envelopeMetadata(query, result, opts),
	}, nil
}

// compaction finalises the streamed compaction, or returns nil when the
// invocation did not ask for one (the same condition compactionFor applies to a
// buffered result).
func (c *streamCollector) compaction() *output.Compaction {
	if !c.opts.Compact || c.opts.JQFilter != "" || c.compact == nil {
		return nil
	}
	fin := c.compact.Finalize()
	return &fin
}
