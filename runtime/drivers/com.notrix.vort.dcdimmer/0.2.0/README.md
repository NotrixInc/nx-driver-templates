# Notrix Vort DC Dimmer Driver (DEVICE)

## Build
`make build`

## Run (dev)
Create a config file:

```json
{ "ip": "192.168.1.50", "port": 80, "username": "", "password": "", "poll_interval_ms": 2000, "channels": 10 }
```

## Driver-to-driver messaging (optional)

This driver can participate in controller-core driver messaging using driver id `com.notrix.vort.dcdimmer`.

- Inbound: accepts `dimming.set` / `dimming.changed` messages and applies brightness.
- Outbound: broadcasts `dimming.changed` when brightness changes locally.

Controller-core enforces a strict allow-list. Create a `DRIVER_TO_DRIVER` binding allowing messages between drivers (and set `bidirectional: true` for two-way).