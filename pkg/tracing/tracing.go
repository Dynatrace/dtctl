// Package tracing configures OpenTelemetry for dtctl using the OTLP HTTP exporter.
//
// OneAgent does not reliably instrument short-lived processes; dtctl therefore uses the
// OpenTelemetry SDK directly so spans are exported before the process exits.
//
// # Span lifecycle
//
// Init creates a single root span covering the entire CLI invocation and registers a
// SimpleSpanProcessor (synchronous exporter) backed by an OTLP HTTP exporter (when
// configured). A synchronous processor is used instead of a batching one because dtctl
// is a short-lived process that produces only a single span — the OTel documentation
// recommends SimpleSpanProcessor for this use case. The caller MUST defer the returned
// shutdown function so the span is ended and the provider is cleanly shut down.
//
// # W3C Trace Context propagation
//
// TRACEPARENT and TRACESTATE environment variables are read on startup. When a valid
// TRACEPARENT is present, the CLI span becomes a child of the caller's span, allowing
// dtctl to participate in an existing distributed trace (e.g. from a CI pipeline or
// orchestrator). Invalid values are silently ignored per the W3C spec: a new root span
// is created instead.
//
// # OTLP export
//
// An OTLP HTTP exporter is initialised only when OTEL_EXPORTER_OTLP_ENDPOINT is set.
// All standard OTEL_* env vars (OTEL_EXPORTER_OTLP_HEADERS, OTEL_SERVICE_NAME, etc.)
// are forwarded to the SDK automatically. When no endpoint is configured the SDK still
// generates valid span contexts that are propagated to Dynatrace API requests via the
// traceparent header, but no spans are exported. The overhead in this case is negligible
// (no network I/O, only in-memory span context generation).
package tracing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/Dynatrace/dtctl/pkg/version"
)

const tracerName = "dtctl"

// Init initialises OpenTelemetry for the current dtctl invocation and returns:
//   - a context carrying the root CLI span (use as parent for all further work)
//   - a shutdown function that MUST be deferred to flush spans before process exit
//   - a non-nil error only when OTEL_EXPORTER_OTLP_ENDPOINT is set but the exporter
//     cannot be created; the returned context and span are always valid
//
// safeArgs are the sanitised command-line arguments (verb + resource only, no flags or
// IDs) used for the span name and the process.command_args resource attribute.
//
// verbosity controls OTel-internal error output: at level 0 SDK-internal errors
// (e.g. failed span exports) are silently discarded; at level 1+ they are printed
// to stderr. This avoids noisy output in normal CLI usage when an OTLP endpoint is
// misconfigured.
func Init(ctx context.Context, spanName string, safeArgs []string, verbosity int) (context.Context, func(context.Context), error) {
	svcName := os.Getenv("OTEL_SERVICE_NAME")
	if svcName == "" {
		svcName = tracerName
	}

	// Build resource attributes: service identity + process metadata per OTel
	// semantic conventions (https://opentelemetry.io/docs/specs/semconv/resource/process/).
	resAttrs := []attribute.KeyValue{
		semconv.ServiceName(svcName),
		semconv.ServiceVersion(version.Version),
		semconv.ProcessCommand("dtctl"),
	}
	if len(safeArgs) > 0 {
		resAttrs = append(resAttrs, semconv.ProcessCommandArgs(safeArgs...))
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			resAttrs...,
		),
	)
	if err != nil {
		// Merge failed (e.g. schema URL conflict between Default and the custom
		// resource). Fall back to the service attributes alone so that
		// service.name and service.version are still present in spans rather
		// than being silently dropped by falling back to resource.Default().
		res = resource.NewWithAttributes(
			semconv.SchemaURL,
			resAttrs...,
		)
	}

	// Route SDK-internal errors (e.g. failed span exports) to stderr only when
	// verbose output is enabled. At verbosity 0 errors are silently discarded
	// to avoid noisy output when an OTLP endpoint is misconfigured.
	if verbosity > 0 {
		otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
			fmt.Fprintf(os.Stderr, "dtctl: otel: %v\n", err)
		}))
	} else {
		otel.SetErrorHandler(otel.ErrorHandlerFunc(func(_ error) {}))
	}

	// W3C TraceContext + Baggage propagators — registered globally so the OTel
	// HTTP instrumentation middleware and manual carrier injections both work.
	prop := propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
	otel.SetTextMapPropagator(prop)

	tpOpts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	}

	// Set up OTLP HTTP exporter only when the endpoint is explicitly configured.
	// All OTEL_EXPORTER_OTLP_* env vars are picked up by the SDK automatically.
	var exportErr error
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		exp, expErr := otlptracehttp.New(ctx)
		if expErr != nil {
			exportErr = fmt.Errorf("OTLP exporter: %w", expErr)
		} else {
			// SimpleSpanProcessor (synchronous) is recommended by OTel for
			// short-lived processes like CLIs. It exports each span immediately
			// when End() is called, so spans are never lost if the process is
			// killed before a deferred flush runs. Since dtctl produces only a
			// single root span, the synchronous overhead is negligible.
			tpOpts = append(tpOpts, sdktrace.WithSyncer(exp))
		}
	}

	tp := sdktrace.NewTracerProvider(tpOpts...)
	otel.SetTracerProvider(tp)

	// Inherit caller's trace context from TRACEPARENT / TRACESTATE env vars.
	// The W3C propagator silently ignores invalid values → new root span.
	parentCtx := prop.Extract(ctx, envCarrier{})

	// Root span covers the entire CLI invocation. SpanKindClient is used because
	// dtctl is a client making outgoing HTTP calls to Dynatrace APIs.
	spanCtx, span := tp.Tracer(tracerName).Start(parentCtx, spanName,
		trace.WithSpanKind(trace.SpanKindClient),
	)

	shutdown := func(ctx context.Context) {
		span.End()
		if err := tp.ForceFlush(ctx); err != nil && !isContextErr(err) {
			fmt.Fprintf(os.Stderr, "dtctl: otel: flush: %v\n", err)
		}
		if err := tp.Shutdown(ctx); err != nil && !isContextErr(err) {
			fmt.Fprintf(os.Stderr, "dtctl: otel: shutdown: %v\n", err)
		}
	}

	return spanCtx, shutdown, exportErr
}

// envCarrier implements propagation.TextMapCarrier backed by environment variables,
// mapping the canonical W3C header names to TRACEPARENT / TRACESTATE.
type envCarrier struct{}

func (envCarrier) Get(key string) string {
	switch strings.ToLower(key) {
	case "traceparent":
		return os.Getenv("TRACEPARENT")
	case "tracestate":
		return os.Getenv("TRACESTATE")
	}
	return ""
}

func (envCarrier) Set(string, string) {}
func (envCarrier) Keys() []string     { return []string{"traceparent", "tracestate"} }

// isContextErr returns true if err is a context.Canceled or context.DeadlineExceeded
// error (or wraps one). These are expected during shutdown when the flush timeout
// expires and should not be surfaced to the user as noisy stderr output.
func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
