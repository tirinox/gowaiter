# HTTP API contract

This document records the compatibility baseline for the existing API and the
outbound network policy enforced for callbacks and periodic tasks.

> [!IMPORTANT]
> Application-level address filtering is enforced, but deployment must still
> place the service in an isolated Docker network. Private address ranges may
> include infrastructure other than Docker when the host routes to it.

## Service endpoint

The service listens on `:10025` by default. The `BIND` environment variable can
override the complete listen address.

All current operations use the `/` path. Request and response bodies use JSON.
Timers exist only in process memory and are lost when the service restarts.

## Create or replace a timer

```http
POST /
Content-Type: application/json

{
  "tag": "refresh-menu",
  "delay": 30,
  "url": "http://nginx/erudite/api/refresh"
}
```

Fields:

- `tag`: logical timer name. Only one active timer may exist for a tag.
- `delay`: number of seconds before the callback starts.
- `url`: URL requested when the timer fires.

Creating a timer with an existing tag stops and replaces the previous timer.
Timer IDs increase monotonically for the lifetime of the process.

Current successful response (`200 OK`):

```json
{"id": 1}
```

## Delete a timer

```http
DELETE /
Content-Type: application/json

{"tag": "refresh-menu"}
```

Current response when the timer was deleted (`200 OK`):

```json
{"result":"ok","message":"timer deleted","code":0}
```

Current response when the tag does not exist (`200 OK`):

```json
{"result":"error","message":"timer not found","code":2}
```

The legacy `200 OK` status for an unknown tag is intentionally preserved for
existing clients.

## Service information

```http
GET /
```

Successful response (`200 OK`):

```json
{"maxCounter":1,"timersActive":0}
```

- `maxCounter`: highest timer ID allocated since process start.
- `timersActive`: timers currently registered in the scheduler. A timer whose
  callback is still running remains active until the callback finishes, unless
  it is explicitly deleted or replaced.

## Health and readiness

`GET` and `HEAD` are supported for both probe endpoints. Probe responses are
not cacheable.

`GET /healthz` is a liveness check. It returns `200 OK` while the HTTP process
is running:

```json
{"status":"ok"}
```

`GET /readyz` reports whether initialization has completed and the service is
ready to accept timer requests. It returns `200 OK` with
`{"status":"ready"}` during normal operation and `503 Service Unavailable`
with `{"status":"not_ready"}` during shutdown.

Readiness intentionally does not invoke nginx or `cron.php`: that endpoint runs
application work and must not be used as a side-effect-free dependency probe.
The container image uses `/readyz` for its Docker health check.

## Callback behavior

When a timer fires, the service sends an HTTP `GET` request to its URL. A `2xx`
response is considered successful. Response bodies are discarded up to 64 KiB
and always closed. The timer is removed after the callback finishes, regardless
of whether it succeeded.

Each attempt has a 30-second timeout. A callback makes at most three attempts.
The pauses before the second and third attempts are one and two seconds
respectively. Retries occur for network errors and HTTP `408`, `425`, `429`, and
`5xx` responses. Other `3xx` or `4xx` responses fail without retrying.

At most 100 callback or cron operations may execute concurrently across the
whole process. Waiting for a concurrency slot does not consume the 30-second
per-attempt timeout.

Periodic tasks from `cron.json` use the same outbound request behavior. The
first request occurs after one complete configured period. A task never overlaps
with itself: if its previous request is still active when another tick occurs,
that tick is skipped. Separate entries remain independent and may run in
parallel, subject to the shared limit of 100 outbound operations.

The service validates the complete cron configuration before it starts serving
HTTP. A missing file, malformed JSON, unknown fields, a non-positive or
overflowing period, or an invalid task URL prevents startup instead of silently
disabling the periodic work. An empty JSON array explicitly disables cron jobs.
Use `-cron-config` to select a file other than `./cron.json`.

On `SIGINT` or `SIGTERM`, cron tickers stop and active cron HTTP requests are
cancelled before the process exits.

## Request validation

Correct requests retain the legacy status codes and response bodies. Invalid
requests use the same structured error body and may return an HTTP error:

- malformed JSON returns `400 Bad Request`;
- missing or incorrectly typed fields return `400 Bad Request`;
- invalid field values return `400 Bad Request`;
- an unknown timer passed to `DELETE` retains the legacy `200 OK` response;
- request bodies are limited to 64 KiB;
- tags must be non-empty valid UTF-8 and no longer than 256 bytes;
- delays must be integer seconds from 0 through 31,536,000 (365 days);
- URLs must be absolute `http` or `https` URLs, contain no credentials, and be
  no longer than 4,096 bytes;
- at most 10,000 distinct tags may be active; replacing an existing tag remains
  possible at the limit, while a new tag returns `429 Too Many Requests`;
- unsupported HTTP methods return `405 Method Not Allowed`.

Unknown JSON fields remain accepted for compatibility. A request body must
contain exactly one JSON object; trailing JSON values are rejected.

## Outbound URL policy

Callback and cron URLs may use only `http` or `https` and must resolve
exclusively to one of these address groups:

- IPv4 loopback: `127.0.0.0/8`;
- IPv6 loopback: `::1/128`;
- private IPv4: `10.0.0.0/8`, `172.16.0.0/12`, or `192.168.0.0/16`;
- private IPv6 unique-local addresses: `fc00::/7`.

This permits URLs such as:

- `http://localhost:8080/task`;
- `http://127.0.0.1:8080/task`;
- `http://nginx/erudite/api/cron.php`, when Docker DNS resolves `nginx` only to
  an allowed private address;
- `http://worker:9000/run`, when `worker` is attached to the same isolated
  Docker network.

Inside a container, `localhost` and loopback addresses refer to that container,
not to the Docker host.

The following targets are rejected by default:

- public IP addresses and hostnames resolving to public addresses;
- IPv4 link-local addresses such as `169.254.0.0/16`, including common cloud
  metadata endpoints;
- IPv6 link-local addresses such as `fe80::/10`;
- unspecified, multicast, or broadcast addresses;
- URLs containing credentials;
- schemes other than `http` and `https`.

Security checks are applied to the resolved dial address, not only to the
hostname text. A hostname with a mixture of allowed and forbidden addresses is
rejected. IPv4-mapped IPv6 addresses are normalized before validation.

Every redirect target is validated with the same rules. DNS resolution and
dialing are coupled so that DNS rebinding cannot replace a validated private
address with a public or link-local address before connection. Environment HTTP
proxies are disabled so they cannot bypass address validation.

Application-level filtering treats RFC 1918 and IPv6 unique-local ranges as
the default private Docker address space. Deployment must additionally place
the service in an isolated Docker network, because an application cannot prove
that every reachable private address belongs to Docker without unsafe access to
the Docker daemon. A future `ALLOWED_CIDRS` setting may narrow the policy to the
exact subnets assigned by deployment.

No URL restriction is bypassed for entries loaded from `cron.json`.
