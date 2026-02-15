# W2IO Driver

Driver ID: `com.notrix.w2io`

This driver polls a W2IO device over HTTP and publishes the provided telemetry fields as variables.

## Supported API model
The driver maps the fields you provided (GasSensorStatus, GasAlarmMode, CoLimitOfGasSensor, relays, contacts, MQTT/SNMP/network/time/identity fields, etc.) into published variables.

Numeric strings like `24.000000JS:24` are normalized to numbers.

## Config example
```json
{
  "base_url": "http://192.168.7.197",
  "status_path": "/api/status",
  "set_path": "/api/set",
  "auth_type": "none",
  "poll_interval_ms": 2000,
  "request_timeout_seconds": 8
}
```

## Control endpoints
- `refresh_status`: force immediate read from status API.
- `set_value`: write value to API using payload `{ "key": "Relay1Status", "value": true }`.

## Build
```bash
go mod tidy
go build -o bin/driver.exe ./cmd/driver
```
