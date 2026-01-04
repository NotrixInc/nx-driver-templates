package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"time"

	"github.com/yourorg/controller-platform/pkg/driversdk"
	"github.com/yourorg/nxdriver-host-minimal/internal/config"
	"github.com/yourorg/nxdriver-host-minimal/internal/host"
)

func main() {
	instancesPath := flag.String("instances", "", "Path to instances.json")
	driversDir := flag.String("drivers_dir", "./drivers", "Directory containing driver binaries")
	outDir := flag.String("out_dir", "./run", "Work/output directory for instances")
	publisherMode := flag.String("publisher", "file", "Publisher mode: file|noop")
	publisherDir := flag.String("publisher_dir", "./out", "Output directory for file publisher")
	internalSock := flag.String("internal_grpc_sock", "/tmp/nxdriver-host.sock", "Unix socket path for internal gRPC (child->host->hub)")
	flag.Parse()

	if *instancesPath == "" {
		panic("missing -instances")
	}
	b, err := os.ReadFile(*instancesPath)
	if err != nil {
		panic(err)
	}
	var f config.InstancesFile
	if err := json.Unmarshal(b, &f); err != nil {
		panic(err)
	}

	var pub driversdk.Publisher
	switch *publisherMode {
	case "noop":
		pub = driversdk.NewNoopPublisher()
	default:
		pub = host.NewFilePublisher(*publisherDir)
	}

	hostGRPCAddr := "unix:///" + *internalSock

	igrpc := host.NewInternalGRPCServer(*internalSock)
	if err := igrpc.Start(); err != nil {
		panic(err)
	}
	defer igrpc.Stop()

	mgr := host.NewManager(*driversDir, *outDir, pub, hostGRPCAddr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := mgr.StartAll(ctx, f.Instances); err != nil {
		panic(err)
	}

	// Run until killed
	for {
		time.Sleep(1 * time.Second)
	}
}
