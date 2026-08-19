package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// The trace handler is the piece that makes Grafana's log→trace jump work.
// If it silently stops adding trace_id, nothing breaks and no test fails —
// you only notice months later when you need to follow a request and cannot.
// Hence these tests assert on the emitted JSON, not on the type.

func decode(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatal("handler wrote nothing")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("log line is not JSON: %v (%q)", err, line)
	}
	return m
}

func TestTraceHandlerAddsIDsInsideSpan(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(NewTraceHandler(slog.NewJSONHandler(&buf, nil)))

	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()
	otel.SetTracerProvider(tp)

	ctx, span := tp.Tracer("test").Start(context.Background(), "unit")
	log.InfoContext(ctx, "inside")
	span.End()

	m := decode(t, &buf)
	for _, key := range []string{"trace_id", "span_id"} {
		v, ok := m[key].(string)
		if !ok || v == "" {
			t.Fatalf("%s missing from log record: %v", key, m)
		}
	}
	if want := span.SpanContext().TraceID().String(); m["trace_id"] != want {
		t.Fatalf("trace_id = %v, want %s", m["trace_id"], want)
	}
}

func TestTraceHandlerLeavesRecordAloneWithoutSpan(t *testing.T) {
	// A log line outside any span must stay a plain log line. Emitting an
	// all-zero trace_id would be worse than none: it looks like a real ID and
	// links to a trace that does not exist.
	var buf bytes.Buffer
	log := slog.New(NewTraceHandler(slog.NewJSONHandler(&buf, nil)))

	log.InfoContext(context.Background(), "outside")

	m := decode(t, &buf)
	if _, present := m["trace_id"]; present {
		t.Fatalf("trace_id must not be set outside a span: %v", m)
	}
	if m["msg"] != "outside" {
		t.Fatalf("message lost: %v", m)
	}
}

func TestTraceHandlerKeepsAttrsAndGroups(t *testing.T) {
	// WithAttrs/WithGroup must return a wrapped handler, not the inner one —
	// otherwise every logger built via slog.With() quietly loses trace IDs.
	var buf bytes.Buffer
	log := slog.New(NewTraceHandler(slog.NewJSONHandler(&buf, nil))).
		With("service", "unit").WithGroup("req")

	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()
	ctx, span := tp.Tracer("test").Start(context.Background(), "unit")
	log.InfoContext(ctx, "grouped", "path", "/x")
	span.End()

	m := decode(t, &buf)
	if m["service"] != "unit" {
		t.Fatalf("attribute lost: %v", m)
	}
	if _, ok := m["trace_id"].(string); !ok {
		t.Fatalf("trace_id lost after With/WithGroup: %v", m)
	}
	grp, ok := m["req"].(map[string]any)
	if !ok || grp["path"] != "/x" {
		t.Fatalf("group lost: %v", m)
	}
}

func TestTraceHandlerRespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	h := NewTraceHandler(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("Enabled must delegate to the inner handler")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Fatal("Error must stay enabled at Warn level")
	}
}

func TestSetupReturnsShutdown(t *testing.T) {
	// Setup must not require a reachable collector: the exporter connects
	// lazily. A service that refuses to start because the collector is down
	// would turn an observability outage into a service outage.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")

	shutdown, err := Setup(context.Background(), "unit-test")
	if err != nil {
		t.Fatalf("Setup failed with an unreachable collector: %v", err)
	}
	if shutdown == nil {
		t.Fatal("shutdown function must not be nil")
	}
	shutdown(context.Background())
}
