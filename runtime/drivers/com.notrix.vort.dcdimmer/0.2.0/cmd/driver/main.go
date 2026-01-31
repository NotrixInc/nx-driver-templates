package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	driversdk "github.com/NotrixInc/nx-driver-sdk"

	"github.com/NotrixInc/nx-driver-templates/drivers/notrix-vort-dc-dimmer-go/internal/driver"
	"github.com/NotrixInc/nx-driver-templates/drivers/notrix-vort-dc-dimmer-go/internal/publisher"
)

const driverID = "com.notrix.vort.dcdimmer"

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

	var cfg driver.Config
	_ = json.Unmarshal(cfgBytes, &cfg)

	coreAddr := os.Getenv("CORE_GRPC_ADDR")
	if coreAddr == "" {
		coreAddr = os.Getenv("CONTROLLER_CORE_GRPC_ADDR")
	}
	externalDeviceKey := os.Getenv("EXTERNAL_DEVICE_KEY")
	if externalDeviceKey == "" && cfg.IP != "" {
		externalDeviceKey = "ip:" + cfg.IP
	}
	if coreAddr == "" {
		panic("missing CORE_GRPC_ADDR")
	}
	if externalDeviceKey == "" {
		panic("missing EXTERNAL_DEVICE_KEY")
	}

	pub, err := publisher.NewCoreGRPCPublisher(coreAddr, driverID, externalDeviceKey, driversdk.NewStdLogger())
	if err != nil {
		panic(err)
	}
	defer pub.Close()

	// In production, driver-host supplies these deps:
	// - Publisher backed by gRPC CoreService client
	// - structured logger
	// Here we wire placeholders; driver-host will replace it.
	deps := driversdk.Dependencies{
		Publisher: pub,
		Logger:    driversdk.NewStdLogger(),
		Clock:     driversdk.NewSystemClock(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := driver.NewVortDCDimmerDriver(*deviceID)

	if err := d.Init(ctx, deps, driversdk.NewJSONConfig(cfgBytes)); err != nil {
		panic(err)
	}
	if err := d.Start(ctx); err != nil {
		panic(err)
	}

	// Wait for SIGTERM/SIGINT
	sigC := make(chan os.Signal, 1)
	signal.Notify(sigC, syscall.SIGINT, syscall.SIGTERM)

	<-sigC
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	_ = d.Stop(shutdownCtx)
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "stopped"})
}
