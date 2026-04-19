package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
)

// Config carries everything telemetry needs that is sourced from env
// vars — resolved by internal/config/ per STANDARDS.md Part 1.
// telemetry never calls os.Getenv itself (OTEL.md §8.2).
type Config struct {
	ServiceName    string
	ServiceVersion string
	AgentRole      string
	AgentTask      string
	AgentSession   string
	CaptureContent bool
	// OTELLevel filters records sent through the otelslog bridge,
	// independent of the stderr handler (OBSERVABILITY.md §Logger Setup).
	// Empty defaults to info.
	OTELLevel slog.Level
}

var setupOnce sync.Once

// Setup initializes the OTEL SDK and returns the otelslog bridge
// handler for the fan-out in internal/logging/ (OTEL.md §7.1). When
// OTEL_EXPORTER_OTLP_ENDPOINT is unset (or OTEL_SDK_DISABLED=true),
// installs explicit no-op providers and returns (nil, no-op shutdown,
// nil). Setup is safe to call exactly once per process; later calls
// return the no-op shutdown without re-initializing.
func Setup(ctx context.Context, cfg Config) (bridge slog.Handler, shutdown func(context.Context) error, err error) {
	shutdown = func(context.Context) error { return nil }
	setupOnce.Do(func() {
		setIdentity(cfg.AgentRole, cfg.AgentTask, cfg.AgentSession)
		setCaptureContent(cfg.CaptureContent)

		registerPropagator()
		registerErrorHandler()

		if !telemetryEnabled() {
			markDisabled()
			return
		}

		warnIfGRPC()

		res, rerr := buildResource(ctx, cfg)
		if rerr != nil {
			err = rerr
			return
		}

		tp, terr := buildTracerProvider(ctx, res)
		if terr != nil {
			err = fmt.Errorf("tracer provider: %w", terr)
			return
		}
		mp, merr := buildMeterProvider(ctx, res)
		if merr != nil {
			_ = tp.Shutdown(ctx)
			err = fmt.Errorf("meter provider: %w", merr)
			return
		}
		lp, lerr := buildLoggerProvider(ctx, res)
		if lerr != nil {
			_ = mp.Shutdown(ctx)
			_ = tp.Shutdown(ctx)
			err = fmt.Errorf("logger provider: %w", lerr)
			return
		}

		otel.SetTracerProvider(tp)
		otel.SetMeterProvider(mp)

		markEnabled()
		initSchemaMigrationsInstrument()

		bridge = newBridgeHandler(lp, cfg.OTELLevel)
		shutdown = composedShutdown(tp, mp, lp)
	})
	return bridge, shutdown, err
}

// telemetryEnabled mirrors the SDK's gating: any OTEL_EXPORTER_OTLP_*
// endpoint variable activates telemetry. OTEL_SDK_DISABLED forces the
// no-op path regardless. Reading os.Getenv here is acceptable per
// OTEL.md §7.2 — telemetry must observe the SDK env contract directly.
func telemetryEnabled() bool {
	if v := os.Getenv("OTEL_SDK_DISABLED"); strings.EqualFold(v, "true") {
		return false
	}
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" ||
		os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != "" ||
		os.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT") != "" ||
		os.Getenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT") != ""
}

func warnIfGRPC() {
	v := strings.ToLower(os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL"))
	if v == "grpc" || v == "grpc/protobuf" {
		slog.Warn("OTEL_EXPORTER_OTLP_PROTOCOL=grpc not supported; falling back to http/protobuf",
			"protocol", v,
		)
	}
}

func registerPropagator() {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
}

func registerErrorHandler() {
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		slog.Warn("otel internal error", "err", Truncate(err.Error(), 256))
	}))
}

func buildResource(ctx context.Context, cfg Config) (*resource.Resource, error) {
	return resource.New(ctx,
		resource.WithSchemaURL(semconv.SchemaURL),
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
		),
		resource.WithFromEnv(),
		resource.WithProcess(),
		resource.WithHost(),
	)
}

func buildTracerProvider(ctx context.Context, res *resource.Resource) (*sdktrace.TracerProvider, error) {
	exp, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	bsp := sdktrace.NewBatchSpanProcessor(exp, sdktrace.WithBatchTimeout(batchInterval))
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(bsp),
		sdktrace.WithResource(res),
	)
	return tp, nil
}

func buildMeterProvider(ctx context.Context, res *resource.Resource) (*sdkmetric.MeterProvider, error) {
	exp, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return nil, err
	}
	reader := sdkmetric.NewPeriodicReader(exp)
	return sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithResource(res),
	), nil
}

func buildLoggerProvider(ctx context.Context, res *resource.Resource) (*sdklog.LoggerProvider, error) {
	exp, err := otlploghttp.New(ctx)
	if err != nil {
		return nil, err
	}
	proc := sdklog.NewBatchProcessor(exp, sdklog.WithExportInterval(batchInterval))
	return sdklog.NewLoggerProvider(
		sdklog.WithProcessor(proc),
		sdklog.WithResource(res),
	), nil
}

func newBridgeHandler(lp log.LoggerProvider, level slog.Level) slog.Handler {
	bridge := otelslog.NewHandler("dept.quest", otelslog.WithLoggerProvider(lp))
	return &levelGatedHandler{inner: bridge, level: level}
}

// levelGatedHandler filters slog records below `level` before passing
// them to the inner handler. The otelslog bridge does not expose a
// level option (OBSERVABILITY.md §Logger Setup pins independent
// thresholds for stderr and the OTEL bridge), so this thin wrapper
// implements the gate.
type levelGatedHandler struct {
	inner slog.Handler
	level slog.Level
}

func (h *levelGatedHandler) Enabled(ctx context.Context, lvl slog.Level) bool {
	if lvl < h.level {
		return false
	}
	return h.inner.Enabled(ctx, lvl)
}

func (h *levelGatedHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level < h.level {
		return nil
	}
	return h.inner.Handle(ctx, r)
}

func (h *levelGatedHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &levelGatedHandler{inner: h.inner.WithAttrs(attrs), level: h.level}
}

func (h *levelGatedHandler) WithGroup(name string) slog.Handler {
	return &levelGatedHandler{inner: h.inner.WithGroup(name), level: h.level}
}

func composedShutdown(tp *sdktrace.TracerProvider, mp *sdkmetric.MeterProvider, lp *sdklog.LoggerProvider) func(context.Context) error {
	var once sync.Once
	var ferr error
	return func(ctx context.Context) error {
		once.Do(func() {
			ferr = errors.Join(
				tp.Shutdown(ctx),
				mp.Shutdown(ctx),
				lp.Shutdown(ctx),
			)
		})
		return ferr
	}
}
