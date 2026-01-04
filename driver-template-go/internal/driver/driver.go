package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/NotrixInc/nx-driver-sdk"
)

type ExampleIPDriver struct {
	deviceID string

	deps   driversdk.Dependencies
	cfg    Config
	client *Client

	stopOnce sync.Once
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

func NewExampleIPDriver(deviceID string) *ExampleIPDriver {
	return &ExampleIPDriver{
		deviceID: deviceID,
		stopCh:  make(chan struct{}),
	}
}

func (d *ExampleIPDriver) ID() string            { return "com.example.template.ip-device" }
func (d *ExampleIPDriver) Version() string       { return "1.0.0" }
func (d *ExampleIPDriver) Type() driversdk.DriverType { return driversdk.DriverTypeDevice }
func (d *ExampleIPDriver) Protocols() []driversdk.Protocol { return []driversdk.Protocol{driversdk.ProtocolIP} }
func (d *ExampleIPDriver) Topologies() []driversdk.Topology { return []driversdk.Topology{driversdk.TopologyDirectIP} }

func (d *ExampleIPDriver) Init(ctx context.Context, deps driversdk.Dependencies, cfg driversdk.JsonConfig) error {
	d.deps = deps

	if err := cfg.Decode(&d.cfg); err != nil {
		return err
	}
	if d.cfg.IP == "" {
		return fmt.Errorf("config.ip is required")
	}
	if d.cfg.PollIntervalSeconds <= 0 {
		d.cfg.PollIntervalSeconds = 5
	}

	d.client = NewClient(d.cfg.IP, d.cfg.Port, d.cfg.Token)
	return nil
}

func (d *ExampleIPDriver) Endpoints() ([]driversdk.Endpoint, error) {
	valueSchema, _ := json.Marshal(map[string]any{"type": "boolean"})
	return []driversdk.Endpoint{
		{
			Key:        "power",
			Name:       "Power",
			Direction:  "BIDIR",
			Type:       "switch",
			ValueSchema: valueSchema,
			Meta:       map[string]string{},
		},
	}, nil
}

func (d *ExampleIPDriver) Variables() ([]driversdk.Variable, error) {
	return []driversdk.Variable{
		{Key: "rssi", Type: "integer", Unit: "dBm", ReadOnly: true},
	}, nil
}

func (d *ExampleIPDriver) Start(ctx context.Context) error {
	// Upsert device + descriptors (driver-host typically handles this once per instance)
	desc := driversdk.DeviceDescriptor{
		DeviceID:          d.deviceID,
		DriverID:          d.ID(),
		ExternalDeviceKey: "ip:" + d.cfg.IP, // stable identity key for direct IP
		DisplayName:       "Example IP Device",
		DeviceType:        "switch",
		Manufacturer:      "Example",
		Model:             "Template",
		Firmware:          "",
		IPAddress:         d.cfg.IP,
		ConnectionCategory: "DIRECT_IP",
		Protocol:           "IP",
		Meta:              map[string]string{},
	}
	_ = d.deps.Publisher.UpsertDevice(ctx, desc)

	eps, _ := d.Endpoints()
	_ = d.deps.Publisher.UpsertEndpoints(ctx, d.deviceID, eps)

	vars, _ := d.Variables()
	_ = d.deps.Publisher.UpsertVariables(ctx, d.deviceID, vars)

	// Poll loop
	d.wg.Add(1)
	go d.pollLoop()

	return nil
}

func (d *ExampleIPDriver) pollLoop() {
	defer d.wg.Done()

	t := time.NewTicker(time.Duration(d.cfg.PollIntervalSeconds) * time.Second)
	defer t.Stop()

	for {
		select {
		case <-d.stopCh:
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			on, err := d.client.GetPower(ctx)
			cancel()

			if err != nil {
				d.deps.Logger.Warn("poll failed", "err", err.Error())
				continue
			}

			state, _ := json.Marshal(map[string]any{
				"power": on,
			})
			_ = d.deps.Publisher.PublishState(context.Background(), driversdk.StateUpdate{
				DeviceID: d.deviceID,
				State:    state,
				At:       d.deps.Clock.Now(),
			})
		}
	}
}

func (d *ExampleIPDriver) HandleCommand(ctx context.Context, cmd driversdk.Command) (driversdk.CommandResult, error) {
	switch cmd.EndpointKey {
	case "power":
		var on bool
		if err := json.Unmarshal(cmd.Payload, &on); err != nil {
			return driversdk.CommandResult{Success: false, Message: "invalid payload"}, err
		}
		if err := d.client.SetPower(ctx, on); err != nil {
			return driversdk.CommandResult{Success: false, Message: err.Error()}, err
		}
		// Publish immediate state update
		state, _ := json.Marshal(map[string]any{"power": on})
		_ = d.deps.Publisher.PublishState(ctx, driversdk.StateUpdate{
			DeviceID: d.deviceID,
			State:    state,
			At:       d.deps.Clock.Now(),
		})
		return driversdk.CommandResult{Success: true, Message: "ok"}, nil
	default:
		return driversdk.CommandResult{Success: false, Message: "unknown endpoint"}, fmt.Errorf("unknown endpoint: %s", cmd.EndpointKey)
	}
}

func (d *ExampleIPDriver) Health(ctx context.Context) (driversdk.HealthStatus, map[string]string) {
	// You can implement a ping or lightweight GET here
	return driversdk.HealthOK, map[string]string{"ip": d.cfg.IP}
}

func (d *ExampleIPDriver) Stop(ctx context.Context) error {
	d.stopOnce.Do(func() { close(d.stopCh) })
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}
