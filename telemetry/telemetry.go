// Package telemetry initializes the OpenTelemetry SDK for this service.
// It wires up:
//   - Traces  → OTLP/HTTP → Alloy (sve24-alloy:4318) → Tempo
//   - Metrics → Prometheus exporter → /metrics endpoint → Prometheus
//   - Logs    → slog JSON with trace_id/span_id injected from context
package telemetry

import (
	"context"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// Setup initialises the global OTel TracerProvider and MeterProvider.
// Call the returned shutdown function in your main() defer chain.
// Reads OTEL_EXPORTER_OTLP_ENDPOINT (default: http://sve24-alloy:4318).
func Setup(ctx context.Context, serviceName string) (func(context.Context), error) {
	// NewSchemaless (no SchemaURL) so Merge cannot hit a schema-URL conflict
	// with resource.Default(): the SDK's default resource carries its own, newer
	// semconv schema URL, and merging two differing schema URLs returns an error
	// that would make Setup() — and therefore the whole service — fail to start.
	res, err := resource.Merge(
		resource.Default(),
		resource.NewSchemaless(
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return nil, err
	}

	// ── Traces ────────────────────────────────────────────────────────────────
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://sve24-alloy:4318"
	}
	traceExp, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(endpoint))
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	// W3C trace-context + baggage propagation. Without this the global
	// propagator stays a no-op: incoming `traceparent` headers are not read and
	// outgoing ones are not set → traces break at every service boundary.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// ── Metrics (Prometheus default registry) ─────────────────────────────────
	metricsExp, err := otelprom.New()
	if err != nil {
		return nil, err
	}
	mp := metric.NewMeterProvider(
		metric.WithReader(metricsExp),
		metric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

	return func(ctx context.Context) {
		_ = tp.Shutdown(ctx)
		_ = mp.Shutdown(ctx)
	}, nil
}

// TraceHandler wraps a slog.Handler and injects trace_id + span_id from the
// request context into every log record. This enables Grafana's Loki→Tempo
// correlation: clicking trace_id in a log line opens the matching trace.
//
// The IDs must sit at the TOP level of the record. slog applies WithAttrs and
// WithGroup in call order, so attributes added from Handle after a WithGroup
// would be nested inside that group — emitted as `req.trace_id`, where the
// Grafana derived field does not look. Nothing fails in that case; the jump
// from log to trace just silently stops working.
//
// Therefore the group and attribute calls are recorded rather than applied,
// and replayed per record on top of a handler that already carries the trace
// attributes. The replay costs one handler chain per logged record, and only
// for loggers that actually use With/WithGroup.
type TraceHandler struct {
	inner slog.Handler
	ops   []handlerOp
}

// handlerOp is one recorded WithAttrs or WithGroup call. Exactly one field is
// set: attrs for WithAttrs, group for WithGroup.
type handlerOp struct {
	attrs []slog.Attr
	group string
}

func NewTraceHandler(inner slog.Handler) *TraceHandler {
	return &TraceHandler{inner: inner}
}

// replay applies the recorded With* calls in their original order.
func (h *TraceHandler) replay(base slog.Handler) slog.Handler {
	for _, op := range h.ops {
		if op.group != "" {
			base = base.WithGroup(op.group)
			continue
		}
		base = base.WithAttrs(op.attrs)
	}
	return base
}

func (h *TraceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *TraceHandler) Handle(ctx context.Context, r slog.Record) error {
	base := h.inner
	if span := trace.SpanFromContext(ctx); span.IsRecording() {
		sc := span.SpanContext()
		base = base.WithAttrs([]slog.Attr{
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		})
	}
	return h.replay(base).Handle(ctx, r)
}

func (h *TraceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &TraceHandler{inner: h.inner, ops: append(append([]handlerOp(nil), h.ops...), handlerOp{attrs: attrs})}
}

func (h *TraceHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &TraceHandler{inner: h.inner, ops: append(append([]handlerOp(nil), h.ops...), handlerOp{group: name})}
}
