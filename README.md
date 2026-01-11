# nx-driver-templates

Reference templates + a minimal host for building NX drivers using `github.com/NotrixInc/nx-driver-sdk`.

## Repository structure

### Templates
- [driver-template-go](driver-template-go) — **DEVICE** (direct IP) template in Go.
- [driver-template-hub-go](driver-template-hub-go) — **HUB** template in Go (shows host internal gRPC hub session + proxying).
- [driver-template-child-go](driver-template-child-go) — **CHILD** template in Go (shows calling host proxy to reach HUB).

### Minimal host
- [driver-host-minimal](driver-host-minimal) — minimal driver-host reference that:
  - launches driver binaries (one process per instance)
  - passes `-device_id` and `-config <path>` to the driver
  - supports a file publisher for local inspection
  - supports hub/child internal gRPC routing

### Demo assets
- [demo](demo)
  - `api.json` — example device status payload (used for mapping fields)
  - `run_demo.sh` — builds hub+child templates and runs the minimal host demo (bash)

## Prerequisites
- Go `1.22+`
- The SDK checked out next to this repo (required by the `replace` directives):
  - `../../nx-driver-sdk` relative to each template folder

On Windows, the `demo/run_demo.sh` script requires WSL or Git Bash.

## Create a new driver from a template

### 1) Direct-IP / standalone device (DEVICE)
1. Copy the template folder:
	- Copy [driver-template-go](driver-template-go) to a new folder (example: `driver-yourdevice-go`).
2. Update identifiers:
	- `manifest.json`: set `id`, `name`, `version`, and update any device metadata used by your platform.
	- `internal/driver/driver.go`: update `ID()` and `Version()` to match.
3. Define config:
	- Edit `internal/driver/config.go` and [driver-template-go/config.schema.json](driver-template-go/config.schema.json).
4. Implement the device protocol:
	- Replace the stub client in `internal/driver/httpclient.go` (or your own `client.go`) with real HTTP/MQTT/etc.
5. Map endpoints and telemetry:
	- Define endpoints in `Endpoints()` and variables in `Variables()`.
	- In the poll loop, publish state/variables only when changed (to avoid noise).
6. Build:
	- `go build -o bin/driver ./cmd/driver`

### 2) Hub + Child topology
- HUB: start from [driver-template-hub-go](driver-template-hub-go)
- CHILD: start from [driver-template-child-go](driver-template-child-go)

This pair demonstrates the host-mediated proxy flow:
- CHILD → host `HostProxyService.ProxyCommand` → HUB session stream `OpenHubSession` → response routed back.

## Run the minimal host

### Build host
From [driver-host-minimal](driver-host-minimal):
- `go build ./cmd/driver-host`

### Run host with instances
Use [driver-host-minimal/instances.example.json](driver-host-minimal/instances.example.json) as a starting point.

## Notes
- The Go modules use a local `replace github.com/NotrixInc/nx-driver-sdk => ../../nx-driver-sdk` so you can develop SDK + drivers together.
- Keep driver IDs stable (reverse-DNS style), e.g. `com.notrix.vort.dcdimmer`.

