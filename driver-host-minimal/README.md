# Minimal Driver Host Router (Go)

Last Updated: 2026-01-03

This is a minimal reference implementation of a **driver-host router** that:
- Launches driver binaries as OS processes (one per instance)
- Provides a JSON config file path and device_id to the driver process
- Implements `driversdk.Publisher` for local/dev (Noop or file-backed)
- Implements `driversdk.HubProxy` routing so CHILD drivers can call the correct HUB instance

## What this is (and is not)
- This is **not** a complete production host (no sandboxing/cgroups, no remote install, no signatures).
- It is intended as the **foundation** for your production driver-host.

## How it works (minimal contract)
- The host starts drivers using the CLI interface:
  - `-device_id <uuid>`
  - `-config <path-to-config.json>`
- HUB instances are registered in an in-memory map by `hubDeviceID`
- CHILD instances are provided a `HubProxy` that routes to the hub instance in the same host process

## Build
```bash
cd driver-host-minimal
go build ./cmd/driver-host
```

## Run (example)
```bash
./driver-host \
  -instances ./instances.json \
  -drivers_dir ../drivers \
  -log_level info
```

`instances.json` example:
```json
{
  "instances": [
    {
      "instance_id": "hub-1",
      "device_id": "11111111-1111-1111-1111-111111111111",
      "driver_id": "com.vendor.zigbee.gateway",
      "driver_type": "HUB",
      "binary": "zigbee-hub-driver",
      "config": { "ip": "192.168.1.10", "api_key": "secret" }
    },
    {
      "instance_id": "child-1",
      "device_id": "22222222-2222-2222-2222-222222222222",
      "driver_id": "com.vendor.zigbee.dimmer",
      "driver_type": "CHILD",
      "binary": "zigbee-dimmer-driver",
      "config": { "hub_device_id": "11111111-1111-1111-1111-111111111111", "child_ref": "0x00158d0001a2b3c4" }
    }
  ]
}
```

## Next steps for production
- Replace the dev `Publisher` with a gRPC CoreService client (controller-core).
- Add process supervision, exponential backoff restart, per-driver resource limits, and structured log shipping.


## Internal gRPC routing (Option 1)
- Host listens on a Unix socket (default: `/tmp/nxdriver-host.sock`).
- CHILD drivers call `HostProxyService.ProxyCommand` on the host.
- HUB drivers open a bidirectional stream `HubGatewayService.OpenHubSession` and **register first** by sending a message with:
  - `EndpointKey = "__register__"`
  - `HubDeviceId = <hub_device_uuid>`
  - `CorrelationId` set
- The host routes subsequent proxy commands to that hub session and returns the response.

The host passes the address to drivers via env var:
- `NX_HOST_GRPC_ADDR=unix:////tmp/nxdriver-host.sock`


### Internal RPC location
The internal gRPC contract is published in the SDK at:
- `pkg/driversdk/hostrpc`

Drivers should import host RPC types from the SDK, not from the host implementation.


## Demo (hub + child)
1. Build the hub and child drivers (see template READMEs):
   - hub binary: `hub-driver`
   - child binary: `child-driver`
2. Put both binaries into a shared `drivers/` folder next to the host.
3. Run the host using the provided demo instances file:
```bash
./driver-host -instances ./instances.demo.json -drivers_dir ./drivers -publisher file -publisher_dir ./out
```
4. Verify:
- Host creates internal gRPC socket `/tmp/nxdriver-host.sock`
- HUB registers a session
- CHILD commands route to HUB through host

To send a command in this demo, use the CHILD driver’s built-in behavior (see template README) or wire it to your controller API.
