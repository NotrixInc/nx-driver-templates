package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"os"

	driversdk "github.com/NotrixInc/nx-driver-sdk"
)

type ExampleChildDriver struct {
	hostProxy *driversdk.HostProxyClient

	deviceID string
	deps     driversdk.Dependencies
	cfg      Config

	hub      driversdk.HubProxy
	childRef string

	stopOnce sync.Once
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

func NewExampleChildDriver(deviceID string) *ExampleChildDriver {
	return &ExampleChildDriver{
		deviceID: deviceID,
		stopCh:   make(chan struct{}),
	}
}

func (d *ExampleChildDriver) ID() string                 { return "com.example.template.child.zigbee-dimmer" }
func (d *ExampleChildDriver) Version() string            { return "1.0.0" }
func (d *ExampleChildDriver) Type() driversdk.DriverType { return driversdk.DriverTypeChild }
func (d *ExampleChildDriver) Protocols() []driversdk.Protocol {
	return []driversdk.Protocol{driversdk.ProtocolZigbee}
}
func (d *ExampleChildDriver) Topologies() []driversdk.Topology {
	return []driversdk.Topology{driversdk.TopologyViaHub}
}

func (d *ExampleChildDriver) Init(ctx context.Context, deps driversdk.Dependencies, cfg driversdk.JSONConfig) error {
	d.deps = deps
	if err := cfg.Decode(&d.cfg); err != nil {
		return err
	}
	if d.cfg.PollIntervalSeconds <= 0 {
		d.cfg.PollIntervalSeconds = 5
	}
	d.childRef = d.cfg.ChildRef
	return nil
}

// BindHub is called by the host to inject the hub proxy and child reference.
func (d *ExampleChildDriver) BindHub(ctx context.Context, hub driversdk.HubProxy, childRef string) error {
	d.hub = hub
	if childRef != "" {
		d.childRef = childRef
	}
	return nil
}

func (d *ExampleChildDriver) Endpoints() ([]driversdk.Endpoint, error) {
	sch, _ := json.Marshal(map[string]any{"type": "boolean"})
	return []driversdk.Endpoint{
		{Key: "power", Name: "Power", Direction: driversdk.EndpointDirectionOutput, Kind: driversdk.EndpointKindControl, Connection: driversdk.EndpointConnectionZigBee, Icon: "zigbee", MultiBinding: false, ControlType: "switch", ValueSchema: sch},
	}, nil
}

func (d *ExampleChildDriver) Variables() ([]driversdk.Variable, error) {
	return []driversdk.Variable{
		{Key: "lqi", Type: driversdk.VariableTypeNumber, Unit: "", Readable: true, Writable: false},
	}, nil
}

func (d *ExampleChildDriver) Start(ctx context.Context) error {
	// Bind hub proxy via host internal gRPC
	if d.hub == nil {
		addr, err := driversdk.RequireHostAddrFromEnv(os.Getenv)
		if err != nil {
			return err
		}
		hp, err := driversdk.DialHostProxy(addr)
		if err != nil {
			return err
		}
		d.hostProxy = hp
		d.hub = &hostHubProxy{client: hp, hubDeviceID: d.cfg.HubDeviceID}
	}

	desc := driversdk.DeviceDescriptor{
		DeviceID:           d.deviceID,
		DriverID:           d.ID(),
		ExternalDeviceKey:  "zb:" + d.childRef,
		DisplayName:        "Example Zigbee Child",
		DeviceType:         "switch",
		Manufacturer:       "Example",
		Model:              "ZB-Dimmer",
		ConnectionCategory: "VIA_HUB",
		Protocol:           "ZIGBEE",
		ParentDeviceID:     d.cfg.HubDeviceID,
		ExternalID:         d.childRef,
	}
	_ = d.deps.Publisher.UpsertDevice(ctx, desc)

	eps, _ := d.Endpoints()
	_ = d.deps.Publisher.UpsertEndpoints(ctx, d.deviceID, eps)

	vars, _ := d.Variables()
	_ = d.deps.Publisher.UpsertVariables(ctx, d.deviceID, vars)

	d.wg.Add(1)
	go d.pollLoop()
	return nil
}

func (d *ExampleChildDriver) pollLoop() {
	defer d.wg.Done()
	t := time.NewTicker(time.Duration(d.cfg.PollIntervalSeconds) * time.Second)
	defer t.Stop()
	for {
		select {
		case <-d.stopCh:
			return
		case <-t.C:
			// Template: publish a heartbeat-ish state; real driver would poll hub or subscribe.
			st, _ := json.Marshal(map[string]any{"online": true})
			_ = d.deps.Publisher.PublishState(context.Background(), driversdk.StateUpdate{
				DeviceID: d.deviceID, State: st, At: d.deps.Clock.Now(),
			})
		}
	}
}

func (d *ExampleChildDriver) HandleCommand(ctx context.Context, cmd driversdk.Command) (driversdk.CommandResult, error) {
	if d.hub == nil {
		return driversdk.CommandResult{Success: false, Message: "hub not bound"}, fmt.Errorf("hub not bound")
	}
	switch cmd.EndpointKey {
	case "power":
		// pass through to hub proxy
		res, err := d.hub.ProxyCommand(ctx, d.childRef, "power", cmd.Payload)
		if err != nil {
			return res, err
		}
		// optimistic state update
		var on bool
		if err := json.Unmarshal(cmd.Payload, &on); err == nil {
			st, _ := json.Marshal(map[string]any{"power": on})
			_ = d.deps.Publisher.PublishState(ctx, driversdk.StateUpdate{
				DeviceID: d.deviceID, State: st, At: d.deps.Clock.Now(),
			})
		}
		return res, nil
	default:
		return driversdk.CommandResult{Success: false, Message: "unknown endpoint"}, fmt.Errorf("unknown endpoint: %s", cmd.EndpointKey)
	}
}

func (d *ExampleChildDriver) Health(ctx context.Context) (driversdk.HealthStatus, map[string]string) {
	if d.childRef == "" {
		return driversdk.HealthDegraded, map[string]string{"reason": "missing childRef"}
	}
	if d.hub == nil {
		return driversdk.HealthDegraded, map[string]string{"reason": "hub not bound"}
	}
	return driversdk.HealthOK, map[string]string{"childRef": d.childRef}
}

func (d *ExampleChildDriver) Stop(ctx context.Context) error {
	if d.hostProxy != nil {
		_ = d.hostProxy.Close()
	}

	d.stopOnce.Do(func() { close(d.stopCh) })
	done := make(chan struct{})
	go func() { d.wg.Wait(); close(done) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

type hostHubProxy struct {
	client      *driversdk.HostProxyClient
	hubDeviceID string
}

func (h *hostHubProxy) ProxyCommand(ctx context.Context, childRef string, endpointKey string, payload []byte) (driversdk.CommandResult, error) {
	// CorrelationID can be generated per call
	cid := "cmd-" + time.Now().Format("150405.000")
	return h.client.ProxyCommand(ctx, h.hubDeviceID, childRef, endpointKey, payload, cid)
}
