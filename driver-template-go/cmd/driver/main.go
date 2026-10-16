package main

import (
	"io"

	driversdk "github.com/NotrixInc/nx-driver-sdk"
	"github.com/NotrixInc/nx-driver-sdk/run"

	"github.com/NotrixInc/nx-driver-templates/driver-template-go/internal/driver"
)

// main is the whole entry point. run.Main runs the driver standalone (one
// device, `-device_id … -config …`) or hosted (every device of the package in
// one process, `-host`), as controller-core chooses, and owns flags, signals,
// panics in Init/Start/Stop, and graceful stop.
//
// Declare `"hosting": "package"` in the manifest's entrypoint so the controller
// runs every device of the package in one process. Keep no per-device state in
// package-level variables: devices share the process.
func main() {
	run.Main(run.Program{
		DriverID: "com.example.ipdevice",
		New: func(d run.Device) (run.Instance, error) {
			return driver.NewExampleIPDriver(d.ID), nil
		},
		Publisher: publisher,
	})
}

// publisher connects a device to controller-core. Replace it with the gRPC
// CoreService publisher the first-party drivers use (internal/publisher in any
// of them: one shared connection per process, Factory for run.Main).
func publisher(coreAddr, driverID, externalKey string, log driversdk.Logger) (driversdk.Publisher, io.Closer, error) {
	return driversdk.NewNoopPublisher(), noClose{}, nil
}

type noClose struct{}

func (noClose) Close() error { return nil }
