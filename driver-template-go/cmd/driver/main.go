package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	// Replace with your published module path
	// "github.com/yourorg/controller-platform/pkg/driversdk"
	"github.com/NotrixInc/NXdriver-SDK/pkg/driversdk"

	"github.com/NotrixInc/NXdriver-SDK/driver-template-go/internal/driver"
)

func main() {
	var (
		deviceID  = flag.String("device_id", "", "Core device UUID (assigned by controller-core)")
		cfgPath   = flag.String("config", "", "Path to config JSON file")
	)
	flag.Parse()

	if *cfgPath == "" {
		panic("missing -config path")
	}

	cfgBytes, err := os.ReadFile(*cfgPath)
	if err != nil {
		panic(err)
	}

	// In production, driver-host supplies these deps:
	// - Publisher backed by gRPC CoreService client
	// - structured logger
	// Here we wire placeholders; driver-host will replace it.
	deps := driversdk.Dependencies{
		Publisher: driversdk.NewNoopPublisher(), // implement in host or replace
		Logger:    driversdk.NewStdLogger(),     // implement in sdk/host
		Clock:     driversdk.NewSystemClock(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := driver.NewExampleIPDriver(*deviceID)

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
