package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	driversdk "github.com/NotrixInc/nx-driver-sdk"

	"github.com/NotrixInc/nx-driver-templates/drivers/com.notrix.camera.isapi/internal/driver"
	"github.com/NotrixInc/nx-driver-templates/drivers/com.notrix.camera.isapi/internal/publisher"
)

func main() {
	fmt.Println("[camera-debug] main() entered")
	var (
		deviceID = flag.String("device_id", "", "Core device UUID (assigned by controller-core)")
		cfgPath  = flag.String("config", "", "Path to config JSON file")
	)
	flag.Parse()

	fmt.Printf("[camera-debug] device_id=%q config=%q\n", *deviceID, *cfgPath)

	if *deviceID == "" {
		panic("missing -device_id")
	}
	if *cfgPath == "" {
		panic("missing -config path")
	}

	cfgBytes, err := os.ReadFile(*cfgPath)
	if err != nil {
		panic(err)
	}

	fmt.Printf("[camera-debug] config read OK (%d bytes)\n", len(cfgBytes))

	log := driversdk.NewStdLogger()

	coreAddr := strings.TrimSpace(os.Getenv("CORE_GRPC_ADDR"))
	if coreAddr == "" {
		coreAddr = strings.TrimSpace(os.Getenv("CONTROLLER_CORE_GRPC_ADDR"))
	}
	if coreAddr == "" {
		coreAddr = strings.TrimSpace(os.Getenv("GRPC_ADDR"))
	}

	driverID := strings.TrimSpace(os.Getenv("DRIVER_ID"))
	if driverID == "" {
		driverID = "com.notrix.camera.isapi"
	}
	externalDeviceKey := strings.TrimSpace(os.Getenv("EXTERNAL_DEVICE_KEY"))
	if externalDeviceKey == "" {
		externalDeviceKey = *deviceID
	}

	fmt.Printf("[camera-debug] coreAddr=%q driverID=%q extKey=%q\n", coreAddr, driverID, externalDeviceKey)
	fmt.Printf("[camera-debug] dialing gRPC...\n")

	pub, err := publisher.NewCoreGRPCPublisher(coreAddr, driverID, externalDeviceKey, log)
	if err != nil {
		panic(err)
	}
	defer func() { _ = pub.Close() }()

	fmt.Printf("[camera-debug] gRPC connected OK\n")

	deps := driversdk.Dependencies{Publisher: pub, Logger: log, Clock: driversdk.NewSystemClock()}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := driver.NewISAPICameraDriver(*deviceID)
	fmt.Printf("[camera-debug] calling Init...\n")
	if err := d.Init(ctx, deps, driversdk.NewJSONConfig(cfgBytes)); err != nil {
		panic(err)
	}
	fmt.Printf("[camera-debug] Init OK, calling Start...\n")
	if err := d.Start(ctx); err != nil {
		panic(err)
	}
	fmt.Printf("[camera-debug] Start OK, waiting for signal...\n")

	sigC := make(chan os.Signal, 1)
	signal.Notify(sigC, syscall.SIGINT, syscall.SIGTERM)
	<-sigC

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	_ = d.Stop(shutdownCtx)
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "stopped"})
}
