# HUB Driver Template (Go) — Demo Ready

Last Updated: 2026-01-03

This HUB template demonstrates:
- Registering a hub session with **driver-host internal gRPC**
- Receiving proxy commands for children
- Returning a response with the same correlation id

## Build
```bash
cd driver-template-hub-go
go build -o ../drivers/hub-driver ./cmd/driver
```

## Run under host
The host will start this binary and set:
- `NX_HOST_GRPC_ADDR` automatically

The HUB will register itself using `device_id` passed by the host.

## Demo behavior
- `DiscoverChildren()` returns one fake child: `child-001`
- `ProxyCommand()` returns an echo response containing:
  - child_ref
  - endpoint_key
  - payload (as bytes) in the response data
