#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DRIVERS_DIR="$ROOT_DIR/drivers"
OUT_DIR="$ROOT_DIR/driver-host-minimal/out"

mkdir -p "$DRIVERS_DIR"

echo "[1/4] Build HUB and CHILD drivers..."
( cd "$ROOT_DIR/driver-template-hub-go" && go build -o "$DRIVERS_DIR/hub-driver" ./cmd/driver )
( cd "$ROOT_DIR/driver-template-child-go" && go build -o "$DRIVERS_DIR/child-driver" ./cmd/driver )

echo "[2/4] Start host..."
NX_DEMO_SEND_COMMAND=1 "$ROOT_DIR/driver-host-minimal/driver-host"   -instances "$ROOT_DIR/driver-host-minimal/instances.demo.json"   -drivers_dir "$DRIVERS_DIR"   -publisher file   -publisher_dir "$OUT_DIR" &

HOST_PID=$!
sleep 2

echo "[3/4] Wait for logs (check host output in console) ..."
sleep 3

echo "[4/4] Stop host"
kill "$HOST_PID" || true
wait "$HOST_PID" || true

echo "Demo completed. Check $OUT_DIR for file-publisher outputs."
