# Mocó compatibility adaptation

This directory preserves the source and Apache-2.0 license of
[`github.com/dagger/otel-go` v1.43.0](https://github.com/dagger/otel-go/tree/v1.43.0).
The original module checksum is
`h1:AYCnAamWmxtSxigWPTgC+8EWqiWPcDZEegh8y05gdJ8=`.
This is a local compatibility adaptation of the Dagger SDK's
telemetry bridge; the OpenTelemetry libraries themselves remain upstream modules.

The original v1.43.0 bridge uses `log.Value` and
`log.KeyValue`, which OpenTelemetry Logs v0.21.0 removed in favor of
`attribute.Value` and `attribute.KeyValue`. Keeping the old bridge unchanged
prevents compilation with the versions that fix GO-2026-6508 and GO-2026-6615.

Local changes are limited to:

- `logging.go`: use unified attribute types for stdio and structured log bodies.
- `transform.go`: migrate OTLP conversion and preserve empty bodies, byte values,
  homogeneous arrays, mixed arrays and maps with the unified value model.
- `go.mod`/`go.sum`: select OpenTelemetry core, trace, SDK and Logs v1.47.0,
  OTLP log exporters v0.23.0 and x/net v0.60.0. These SDK and API versions
  address GO-2026-6974 and GO-2026-6975.
- `compat_test.go`: check value round trips, stdio/EOF records, structured logs,
  trace context, resources, and OTLP export/reexport through the new SDK.

All other Go source files are unchanged from the published version. No generated
Dagger client files are patched. `dagger/go.mod` selects this module through a
local replacement, and the Dagger check includes it in its isolated SDK source.
`make test` runs its tests even when generated Dagger client files are absent.

The upstream v1.47.0 bridge supports the stable Logs API. Its empty-body
conversion and record reexport differ from this adapter in the compatibility
tests. Before removing the replacement, run those tests against a candidate
release using its public OTLP conversion functions. Verify empty and missing
bodies and record round trips, then run the complete `make check`.
