package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// recordSpans installs a recorder as the global tracer provider and returns it.
func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prev)
	})
	return rec
}

func callPath(h http.Handler, path string) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
}

func TestWrapHandlerTracesBusinessRequests(t *testing.T) {
	rec := recordSpans(t)
	h := WrapHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), "unit")

	callPath(h, "/api/things")

	if got := len(rec.Ended()); got != 1 {
		t.Fatalf("expected exactly one span for a business request, got %d", got)
	}
}

func TestWrapHandlerSkipsOperationalPaths(t *testing.T) {
	// Probes run every few seconds per pod and the scrape on its own interval.
	// If these ever start producing spans, Tempo fills with traffic nobody
	// reads and the useful traces get harder to find — a slow degradation that
	// no test would otherwise catch.
	rec := recordSpans(t)
	h := WrapHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), "unit")

	for path := range operationalPaths {
		callPath(h, path)
	}

	if got := len(rec.Ended()); got != 0 {
		t.Fatalf("operational paths must not be traced, got %d spans", got)
	}
}

func TestWrapHandlerPassesTheRequestThrough(t *testing.T) {
	// Filtering must skip the SPAN, not the handler.
	var served []string
	h := WrapHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served = append(served, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	}), "unit")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if len(served) != 1 || served[0] != "/healthz" {
		t.Fatalf("filtered request never reached the handler: %v", served)
	}
	if rec.Code != http.StatusTeapot {
		t.Fatalf("status lost: %d", rec.Code)
	}
}
