package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	driversdk "github.com/NotrixInc/nx-driver-sdk"

	"github.com/NotrixInc/nx-driver-templates/drivers/light_dimmer-go/internal/driver"
	"github.com/NotrixInc/nx-driver-templates/drivers/light_dimmer-go/internal/publisher"
)

func main() {
	var (
		deviceID = flag.String("device_id", "", "Core device UUID (assigned by controller-core)")
		cfgPath  = flag.String("config", "", "Path to config JSON file")
	)
	flag.Parse()

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
		driverID = "light_dimmer"
	}
	externalDeviceKey := strings.TrimSpace(os.Getenv("EXTERNAL_DEVICE_KEY"))
	if externalDeviceKey == "" {
		externalDeviceKey = *deviceID
	}

	pub, err := publisher.NewCoreGRPCPublisher(coreAddr, driverID, externalDeviceKey, log)
	if err != nil {
		panic(err)
	}
	defer func() { _ = pub.Close() }()

	deps := driversdk.Dependencies{
		Publisher: pub,
		Logger:    log,
		Clock:     driversdk.NewSystemClock(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := driver.NewLightDimmerDriver(*deviceID)

	if err := d.Init(ctx, deps, driversdk.NewJSONConfig(cfgBytes)); err != nil {
		panic(err)
	}
	if err := d.Start(ctx); err != nil {
		panic(err)
	}

	sigC := make(chan os.Signal, 1)
	signal.Notify(sigC, syscall.SIGINT, syscall.SIGTERM)
	<-sigC

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	_ = d.Stop(shutdownCtx)
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "stopped"})
}
