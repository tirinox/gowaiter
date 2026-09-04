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
