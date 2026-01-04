# CHILD Driver Template (Go) — Demo Ready

Last Updated: 2026-01-03

This CHILD template demonstrates:
- Dialing the host internal gRPC using `NX_HOST_GRPC_ADDR`
- Calling `HostProxyService.ProxyCommand` to route a command to the HUB instance

## Build
```bash
cd driver-template-child-go
go build -o ../drivers/child-driver ./cmd/driver
```

## Demo command trigger
This template supports a simple environment-variable driven demo command:
- If `NX_DEMO_SEND_COMMAND=1` is set, it will send one command after startup:
  - endpoint: `power`
  - payload: `true` (JSON boolean)
  - child_ref: from config
  - hub_device_id: from config

Run the host with the demo instances file. The host logs should show the routed proxy response.
