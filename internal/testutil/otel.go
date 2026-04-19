// Package testutil — OTEL capturing helpers.
//
// These helpers are the shared exporter setup dance for Phase-12 and
// Phase-13 telemetry tests (per the Phase 12 plan §Shared tracetest
// helper). Each NewCapturingX helper installs an in-memory provider as
// the global, returns a handle to the captured records, and registers
// t.Cleanup to restore the previous global. Tests never write the
// install-and-restore boilerplate inline.
//
// The package is test-only by convention (see doc.go); the OTEL
// import-tripwire in internal/telemetry/telemetry_test.go exempts
// internal/testutil/ explicitly.
package testutil

import (
	"context"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// NewCapturingTracer installs an in-memory tracer provider as the
// global, returns the exporter (callers fetch SpanStubs via
// Exporter.GetSpans), and restores the previous global on test
// completion. Uses a SimpleSpanProcessor so spans are exported
// immediately — no need to wait for batch timeouts in tests.
func NewCapturingTracer(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	prev := otel.GetTracerProvider()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prev)
	})
	return exp
}

// CapturingMeter holds a manual reader and the meter provider it
// drives. Callers Collect() to materialize metric data.
type CapturingMeter struct {
	Reader   *sdkmetric.ManualReader
	Provider *sdkmetric.MeterProvider
}

// Collect drains the manual reader into a ResourceMetrics value. Tests
// then walk Provider.ScopeMetrics → Metrics → Data to assert on
// individual instruments.
func (c *CapturingMeter) Collect(ctx context.Context) metricdata.ResourceMetrics {
	var rm metricdata.ResourceMetrics
	_ = c.Reader.Collect(ctx, &rm)
	return rm
}

// NewCapturingMeter installs an in-memory meter provider with a manual
// reader as the global and restores the previous global on cleanup.
func NewCapturingMeter(t *testing.T) *CapturingMeter {
	t.Helper()
	prev := otel.GetMeterProvider()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(mp)
	t.Cleanup(func() {
		_ = mp.Shutdown(context.Background())
		otel.SetMeterProvider(prev)
	})
	return &CapturingMeter{Reader: reader, Provider: mp}
}

// CapturingLogger captures every log record emitted through its
// LoggerProvider. Tests call Records() to inspect what was emitted.
type CapturingLogger struct {
	Provider *sdklog.LoggerProvider

	mu      sync.Mutex
	records []sdklog.Record
}

// Records returns a snapshot of every record captured so far.
func (c *CapturingLogger) Records() []sdklog.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]sdklog.Record, len(c.records))
	copy(out, c.records)
	return out
}

// NewCapturingLogger constructs an in-memory log provider. Caller plugs
// the provider into the otelslog bridge handler being tested. Records
// emitted through the bridge appear in Records() in emission order.
func NewCapturingLogger(t *testing.T) *CapturingLogger {
	t.Helper()
	cap := &CapturingLogger{}
	cap.Provider = sdklog.NewLoggerProvider(sdklog.WithProcessor(&recorderProcessor{cap: cap}))
	t.Cleanup(func() { _ = cap.Provider.Shutdown(context.Background()) })
	return cap
}

type recorderProcessor struct {
	cap *CapturingLogger
}

func (p *recorderProcessor) OnEmit(_ context.Context, r *sdklog.Record) error {
	p.cap.mu.Lock()
	defer p.cap.mu.Unlock()
	p.cap.records = append(p.cap.records, r.Clone())
	return nil
}

func (p *recorderProcessor) Enabled(_ context.Context, _ sdklog.EnabledParameters) bool {
	return true
}

func (p *recorderProcessor) Shutdown(context.Context) error   { return nil }
func (p *recorderProcessor) ForceFlush(context.Context) error { return nil }
