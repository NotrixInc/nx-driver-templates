# ISAPI Camera Driver (Snapshot + RTSP)

**Driver ID:** `com.notrix.camera.isapi`  
**Version:** `0.1.0`  
**Type:** DEVICE (DIRECT_IP)

## Overview

This driver provides snapshot capture and RTSP streaming support for IP cameras that implement the ISAPI protocol (commonly used by Hikvision and other camera manufacturers).

## Features

- **Snapshot Capture**: Fetches JPEG snapshots via HTTP
- **RTSP Streaming**: Provides RTSP stream URLs for video playback
- **Flexible Authentication**: Supports both Basic and Digest authentication
- **Configurable Ports**: Separate configuration for HTTP snapshot and RTSP streaming ports
- **Resolution Control**: Configurable snapshot resolution

## Configuration

### Required Fields

- **`ip`** (string): Camera IP address or hostname
- **`username`** (string): Camera username for authentication
- **`password`** (string): Camera password for authentication

### Optional Fields

- **`snapshot_port`** (integer, default: 80): HTTP port for snapshot requests
- **`stream_port`** (integer, default: 554): RTSP port for video streaming
- **`auth_type`** (string, default: "basic"): Authentication type - either "basic" or "digest"
- **`snapshot_resolution`** (string, default: "640x480"): Snapshot resolution (e.g., "640x480", "1920x1080")
- **`embed_credentials_in_rtsp_url`** (boolean, default: false): If true, returns RTSP URL with credentials embedded (`rtsp://user:pass@host:port/...`)
- **`request_timeout_seconds`** (integer, default: 8): HTTP request timeout in seconds

### Example Configuration

```json
{
  "ip": "192.168.1.100",
  "snapshot_port": 80,
  "stream_port": 554,
  "username": "admin",
  "password": "admin123",
  "auth_type": "digest",
  "snapshot_resolution": "1280x720",
  "embed_credentials_in_rtsp_url": false,
  "request_timeout_seconds": 10
}
```

## Endpoints

### `get_snapshot`

Fetches a JPEG snapshot from the camera.

**Returns:**
```json
{
  "mime": "image/jpeg",
  "bytes_base64": "<base64-encoded image data>",
  "snapshot_url": "http://192.168.1.100:80/ISAPI/Streaming/channels/1/picture?resolution=640x480"
}
```

### `get_stream_url`

Returns the RTSP stream URL for the camera.

**Returns:**
```json
{
  "rtsp_url": "rtsp://192.168.1.100:554/Streaming/channels/102",
  "rtsp_url_with_credentials": "rtsp://admin:password@192.168.1.100:554/Streaming/channels/102"
}
```

## Variables

- **`snapshot`** (Image): Last captured snapshot data
- **`stream_url`** (Video): RTSP stream URL

## ISAPI URLs

- **Snapshot**: `http://IP:port/ISAPI/Streaming/channels/1/picture?resolution={resolution}`
- **RTSP Stream**: `rtsp://IP:port/Streaming/channels/102`

## Authentication

### Basic Authentication
Standard HTTP Basic auth (Base64-encoded username:password).

**Config:** `"auth_type": "basic"`

### Digest Authentication
HTTP Digest auth with MD5 challenge-response for better security.

**Config:** `"auth_type": "digest"`

## Building

### For Linux
```bash
cd nx-driver-templates/drivers/com.notrix.camera.isapi
GOOS=linux GOARCH=amd64 go build -o bin/driver ./cmd/driver
```

### For Windows
```bash
go build -o bin\driver.exe .\cmd\driver
```

## Packaging

```bash
# From nx-driver-templates/drivers/com.notrix.camera.isapi
make build-linux
make package-linux

# Or manually:
nx-driver-packager pack \
  --input . \
  --out ../../../controller-platform/_tmp/com.notrix.camera.isapi-0.1.0-linux-amd64.nxpkg
```

## Troubleshooting

### 404 - variables.schema.json not found
Ensure the driver package includes all required files:
- manifest.json
- config.schema.json
- endpoints.json
- variables.schema.json
- capabilities.json
- events.json
- bin/driver

Rebuild and repackage from the correct source directory in `nx-driver-templates/drivers/`.

### Authentication Failures
1. Verify username/password
2. Try switching between "basic" and "digest" auth types
3. Check camera web interface accessibility

### Snapshot Failures
- Verify snapshot port (usually 80 or 8000)
- Try different resolutions
- Check ISAPI endpoint support

## Compatibility

Designed for ISAPI-enabled cameras:
- Hikvision IP cameras
- Compatible OEM cameras
- Other ISAPI-enabled devices
