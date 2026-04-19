package telemetry

// MarkEnabledForTest flips the package's enabled flag so other-package
// tests can drive the InstrumentedStore decorator path without running
// the full Setup. Paired with MarkDisabledForTest in defer or t.Cleanup.
// Test-only — production code never imports this.
func MarkEnabledForTest() { markEnabled() }

// MarkDisabledForTest restores the disabled-OTEL state.
func MarkDisabledForTest() { markDisabled() }

// InitInstrumentsForTest registers every dept.quest.* instrument
// against the currently-installed meter provider. Cross-package
// integration tests that swap in a capturing meter via
// testutil.NewCapturingMeter call this so MigrateSpan / RecordX
// recorders write to the captured reader.
func InitInstrumentsForTest() { initSchemaMigrationsInstrument() }
