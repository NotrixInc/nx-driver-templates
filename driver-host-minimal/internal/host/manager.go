package host

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"time"

	"github.com/NotrixInc/nx-driver-sdk"
	"github.com/yourorg/nxdriver-host-minimal/internal/config"
)

// NOTE: This minimal host does not dynamically load Go plugins.
// It runs driver binaries and relies on them to publish to controller-core via Publisher (future gRPC).
// HubProxy routing here is demonstrated by a direct in-process interface; production host will route via RPC
// or by linking drivers as libraries. This file provides the scaffolding for hub registry and instance lifecycle.

type Instance struct {
	Spec config.InstanceSpec
	Proc *Proc
	WorkDir string
}

type Manager struct {
	HostGRPCAddr string

	DriversDir string
	OutDir     string
	Publisher  driversdk.Publisher
	Logger     *StdLogger
	Clock      SystemClock

	Hubs *HubRegistry

	mu sync.Mutex
	instances map[string]*Instance
}

func NewManager(driversDir, outDir string, pub driversdk.Publisher, hostGRPCAddr string) *Manager {
	return &Manager{
		DriversDir: driversDir,
		OutDir: outDir,
		Publisher: pub,
		Logger: NewStdLogger(),
		Clock: SystemClock{},
		Hubs: NewHubRegistry(),
		instances: map[string]*Instance{},
		HostGRPCAddr: hostGRPCAddr,
	}
}

func (m *Manager) StartAll(ctx context.Context, specs []config.InstanceSpec) error {
	for _, s := range specs {
		if err := m.StartOne(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) StartOne(ctx context.Context, s config.InstanceSpec) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.instances[s.InstanceID]; ok {
		return fmt.Errorf("instance already exists: %s", s.InstanceID)
	}

	binPath := filepath.Join(m.DriversDir, s.Binary)
	work := filepath.Join(m.OutDir, s.InstanceID)
	proc := &Proc{}
	if len(s.Config) == 0 {
		s.Config = json.RawMessage(`{}`)
	}
	env := []string{"NX_HOST_GRPC_ADDR=" + m.HostGRPCAddr}
	if err := proc.Start(ctx, StartSpec{
		BinaryPath: binPath,
		DeviceID: s.DeviceID,
		ConfigJSON: s.Config,
		WorkDir: work,
		Env: env,
	}); err != nil {
		return err
	}

	inst := &Instance{Spec: s, Proc: proc, WorkDir: work}
	m.instances[s.InstanceID] = inst

	// Stream logs to host logger (minimal)
	m.pipeLogs("stdout", s.InstanceID, proc.Stdout())
	m.pipeLogs("stderr", s.InstanceID, proc.Stderr())
	return nil
}

func (m *Manager) pipeLogs(stream, instanceID string, r io.ReadCloser) {
	if r == nil { return }
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				m.Logger.Info("driver output", "stream", stream, "instance", instanceID, "msg", string(buf[:n]))
			}
			if err != nil {
				return
			}
		}
	}()
}

func (m *Manager) StopAll(timeout time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, inst := range m.instances {
		_ = inst.Proc.Stop(timeout)
	}
	m.instances = map[string]*Instance{}
}
