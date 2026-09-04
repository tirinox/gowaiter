# HTTP API contract

This document records the compatibility baseline for the existing API and the
outbound network policy that will be enforced during HTTP hardening.

> [!IMPORTANT]
> The outbound URL restrictions below are the target contract. The current
> implementation does not enforce them yet and must not be exposed to untrusted
> clients before that work is complete.

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

The HTTP-hardening stage will change the missing-timer status to `404 Not
Found`, while retaining a structured JSON error body.

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

## Callback behavior

When a timer fires, the service sends an HTTP `GET` request to its URL. The
response body is ignored. The timer is removed after the request finishes,
regardless of whether the request succeeded. There are currently no retries.

Periodic tasks from `cron.json` use the same outbound request behavior. The
first request occurs after one complete configured period.

## Target request validation

The HTTP-hardening stage will enforce the following rules:

- malformed JSON returns `400 Bad Request`;
- missing or incorrectly typed fields return `400 Bad Request`;
- invalid field values return `422 Unprocessable Entity`;
- an unknown timer passed to `DELETE` returns `404 Not Found`;
- request bodies, tag length, delay, and total active timers have explicit
  limits;
- unsupported HTTP methods return `405 Method Not Allowed`.

Exact value limits will be defined alongside the implementation.

## Outbound URL policy

By default, callback and cron URLs may use only `http` or `https` and must
resolve exclusively to one of these address groups:

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

The following targets are rejected by default:

- public IP addresses and hostnames resolving to public addresses;
- IPv4 link-local addresses such as `169.254.0.0/16`, including common cloud
  metadata endpoints;
- IPv6 link-local addresses such as `fe80::/10`;
- unspecified, multicast, or broadcast addresses;
- URLs containing credentials;
- schemes other than `http` and `https`.

Security checks must be applied to the resolved dial address, not only to the
hostname text. A hostname with a mixture of allowed and forbidden addresses is
rejected. IPv4-mapped IPv6 addresses are normalized before validation.

Every redirect target is validated with the same rules. DNS resolution and
dialing must be coupled so that DNS rebinding cannot replace a validated private
address with a public or link-local address before connection.

Application-level filtering treats RFC 1918 and IPv6 unique-local ranges as
the default private Docker address space. Deployment must additionally place
the service in an isolated Docker network, because an application cannot prove
that every reachable private address belongs to Docker without unsafe access to
the Docker daemon. A future `ALLOWED_CIDRS` setting may narrow the policy to the
exact subnets assigned by deployment.

No URL restriction is bypassed for entries loaded from `cron.json`.
