# go-waiter

It is a service that do actions after specified delay.

WORK IN PROGRESS

## Development

The project uses Go 1.26.8. Common commands:

```sh
make deps
make check
make test-race
make build
make run
```

Build and run the container locally:

```sh
make docker-build
make docker-run
```

The service listens on port `10025` by default. Override the host port with
`make docker-run PORT=8080`.

Delayed timers are stored in an embedded BoltDB database. Local runs use
`./gowaiter.db`; set `TIMER_DB` or pass `-timer-db` to choose another path. The
Docker image uses `/data/gowaiter.db`, and `make docker-run` attaches the named
volume `gowaiter_data` by default. Override it with
`make docker-run DATA_VOLUME=my_volume`.

Compose deployments should mount the same path explicitly:

```yaml
services:
  gowaiter:
    volumes:
      - gowaiter_data:/data

volumes:
  gowaiter_data:
```

## Periodic PHP cron

`gowaiter` does not execute PHP itself. Each configured period it performs the
following call chain:

```text
gowaiter -> nginx -> /erudite/api/cron.php -> doCRON()
```

The default [`cron.json`](cron.json) invokes that endpoint every 60 seconds:

```json
[
  {
    "period": 60,
    "task": "http://nginx/erudite/api/cron.php"
  }
]
```

The first request occurs after one complete period. A cron entry never overlaps
with itself, while separate entries may run concurrently. All cron requests use
the same private-network restrictions, timeout, retry policy, and global
100-request concurrency limit as timer callbacks.

The configuration is validated before the HTTP server starts. Use
`gowaiter -cron-config /path/to/cron.json` to select a different file; use an
empty JSON array to run without periodic jobs.

## Documentation

- [HTTP API contract and outbound URL policy](docs/http-api.md)
