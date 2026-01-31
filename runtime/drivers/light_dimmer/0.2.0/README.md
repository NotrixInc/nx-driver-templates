# Light Dimmer Driver (DEVICE)

A minimal, UI-friendly example driver that exposes a single brightness control endpoint.

## Endpoint
- `brightness` (Output, Control, multi_binding: true)
- Value schema: number 0..100

## Config (dev)
Create a config file:
```json
{ "initial_brightness": 75 }
```

## Build
`make build`

## Run (dev)
This is a simulation driver (no real hardware calls). It keeps brightness in memory and publishes state/variable updates.

## Driver-to-driver messaging
This driver also participates in the controller-core driver messaging system:

- Polls `GET /v1/driver-messages?driver_id=light_dimmer`.
- On **user-driven** brightness changes, broadcasts a message `type: "dimming.changed"` with `{ "endpoint_key": "brightness", "value": <0..100> }`.
- On receiving `dimming.changed` / `dimming.set` from other drivers, applies the value locally and publishes it as `SourceDriver`.

Messaging is gated by controller-core bindings:

- Create a binding with `binding_type: "DRIVER_TO_DRIVER"`
- `meta: {"source_driver_id":"light_dimmer","target_driver_id":"<other>","bidirectional":true}`
- `rule_type: "ALLOW"`

Example:
`./bin/driver -device_id <core-device-uuid> -config ./config.json`
