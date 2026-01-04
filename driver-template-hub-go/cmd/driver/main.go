package main

import (
  "context"
  "flag"
  "os"
  "os/signal"
  "syscall"
  "time"

  "github.com/NotrixInc/NXdriver-SDK/pkg/driversdk"
  "github.com/NotrixInc/NXdriver-SDK/driver-template-hub-go/internal/driver"
)

func main() {
  var deviceID = flag.String("device_id", "", "Core hub device UUID")
  var cfgPath  = flag.String("config", "", "Path to config JSON")
  flag.Parse()

  if *cfgPath == "" {
    panic("missing -config")
  }
  cfgBytes, err := os.ReadFile(*cfgPath)
  if err != nil { panic(err) }

  deps := driversdk.Dependencies{
    Publisher: driversdk.NewNoopPublisher(), // driver-host replaces in production
    Logger:    driversdk.NewStdLogger(),
    Clock:     driversdk.NewSystemClock(),
  }

  d := driver.NewExampleHubDriver(*deviceID)

  ctx, cancel := context.WithCancel(context.Background())
  defer cancel()

  if err := d.Init(ctx, deps, driversdk.NewJSONConfig(cfgBytes)); err != nil { panic(err) }
  if err := d.Start(ctx); err != nil { panic(err) }

  sigC := make(chan os.Signal, 1)
  signal.Notify(sigC, syscall.SIGINT, syscall.SIGTERM)
  <-sigC

  shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
  defer shutdownCancel()
  _ = d.Stop(shutdownCtx)
}
