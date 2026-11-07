# Tracing

Quizzotic emits OpenTelemetry traces. With no configuration, tracing is a
no-op: nothing is exported and no exporter is started. It is enabled and tuned
only by the standard `OTEL_*` environment variables; there are no Quizzotic
config keys for it.

## Enabling export

Set an OTLP endpoint on the process you want traced:

```sh
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector.example.com:4318 \
OTEL_SERVICE_NAME=quizzotic \
quizzotic serve --config quizzotic.yaml
```

| Variable | Effect |
| --- | --- |
| `OTEL_EXPORTER_OTLP_ENDPOINT` (or `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`) | Enables export to an OTLP collector. `OTEL_EXPORTER_OTLP_PROTOCOL` picks `http/protobuf` (default) or `grpc`; headers go in `OTEL_EXPORTER_OTLP_HEADERS`. |
| `OTEL_TRACES_EXPORTER` | `otlp`, `console` or `none`. Any value other than `none` enables tracing even without an endpoint. |
| `OTEL_SERVICE_NAME` | Service name on every span. Defaults to `quizzotic`. |
| `OTEL_RESOURCE_ATTRIBUTES` | Extra resource attributes, for example `deployment.environment=prod`. |
| `OTEL_TRACES_SAMPLER` / `OTEL_TRACES_SAMPLER_ARG` | Sampling, for example `parentbased_traceidratio` with `0.1`. |
| `OTEL_SDK_DISABLED` | `true` turns tracing off whatever else is set. |

The client commands (`quizzotic <resource>`, `review`, `login`, `mcp`) read the
same variables, so exporting from them is also opt-in.

## What is traced

One request is one trace, with these spans:

- **HTTP server**: one span per request, named after the matched route
  pattern, with method, path, route and response status. A `traceparent`
  header on the request is honored, so the span joins the caller's trace.
- **Service**: spans for the service-layer operations a request runs.
- **Database**: one span per SQL statement and per transaction
  (`db.transaction`), with `db.system`, operation and table. SQL text and
  arguments are never recorded.
- **OIDC**: `oidc.discovery` and `oidc.jwks` spans for calls to the identity
  provider.
- **Clients**: the CLI and the MCP server start a client span per outbound
  request (method, server address, URL without credentials or query, response
  status) and send `traceparent`, so a CLI or agent call and the server work it
  triggers appear as one trace. The header is sent even when no exporter is
  configured: a span context already present in the call's context is
  forwarded by the W3C propagator, which is always installed.

## Log correlation

When a request is traced, every log line written for it carries `trace_id` and
`span_id`. Search your log store for a `trace_id` to find the lines for a trace,
or take the ID from a log line into your trace viewer. Untraced requests have
no such fields.

## Trying it locally

`mise run dev-otel` starts Grafana, Tempo and a collector in one container, and
`QUIZZOTIC_DEV_OTEL=1 mise run dev` exports to it. See "Local tracing" in
[CONTRIBUTING.md](../CONTRIBUTING.md#local-tracing).
