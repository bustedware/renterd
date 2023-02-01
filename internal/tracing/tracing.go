package tracing

import (
	"context"
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
	"go.opentelemetry.io/otel/trace"
	"go.sia.tech/jape"
)

const (
	service        = "renterd"
	serviceVersion = "0.1.0"
	name           = "renterd"
)

var (
	Tracer = trace.NewNoopTracerProvider().Tracer("noop")
)

// Init initialises a new OpenTelemetry Tracer using information from the
// environment and process. For more information on available environment
// variables for configuration, check out
// https://opentelemetry.io/docs/reference/specification/sdk-environment-variables/.
// https://github.com/open-telemetry/opentelemetry-go/tree/main/exporters/otlp/otlptrace
func Init(workerID string) error {
	// Create resources.
	resources := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceNameKey.String(service),
		semconv.ServiceVersionKey.String(serviceVersion),
		semconv.ServiceInstanceIDKey.String(workerID),
	)
	// Create exporter.
	client := otlptracehttp.NewClient()
	exporter, err := otlptrace.New(context.Background(), client)
	if err != nil {
		return err
	}

	// Create provider
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithResource(resources),
		sdktrace.WithBatcher(exporter),
	)
	otel.SetTracerProvider(provider)

	// Set global tracer.
	Tracer = otel.Tracer(name)

	return nil
}

// TracedHandler is a piece of http middleware that attaches a tracing span to
// every incoming request. The name used for the span is the path of the
// endpoint.
func TracedRoutes(routes map[string]jape.Handler) http.Handler {
	adapt := func(route string, h jape.Handler) jape.Handler {
		return jape.Adapt(func(h http.Handler) http.Handler {
			return otelhttp.NewHandler(h, route)
		})(h)
	}
	for route, handler := range routes {
		routes[route] = adapt(route, handler)
	}
	return jape.Mux(routes)
}
