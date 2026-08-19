package telemetry

import (
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// operationalPaths are endpoints that carry no business information and are
// requested continuously: Kubernetes probes every few seconds per pod, and the
// Prometheus scrape on its own interval. Tracing them floods Tempo with spans
// nobody will ever read and makes the useful ones harder to find.
var operationalPaths = map[string]bool{
	"/healthz": true,
	"/readyz":  true,
	"/livez":   true,
	"/metrics": true,
}

// WrapHandler puts every request through a server span, except the operational
// endpoints above. serviceName becomes the span name prefix and should match
// the name passed to Setup, so traces, metrics and logs agree on who emitted
// them.
//
// This lives here rather than in each service because the filter is a decision,
// not boilerplate: which paths are worth tracing has to be answered the same
// way everywhere, or one service quietly fills Tempo with probe traffic while
// the others do not.
func WrapHandler(h http.Handler, serviceName string) http.Handler {
	return otelhttp.NewHandler(h, serviceName,
		otelhttp.WithFilter(func(req *http.Request) bool {
			return !operationalPaths[req.URL.Path]
		}),
	)
}
