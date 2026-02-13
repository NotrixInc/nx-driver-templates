package driver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	driversdk "github.com/NotrixInc/nx-driver-sdk"
)

type ISAPICameraDriver struct {
	deviceID string
	deps     driversdk.Dependencies
	cfg      Config
	client   *Client
	stopCh   chan struct{}
}

func NewISAPICameraDriver(deviceID string) *ISAPICameraDriver {
	return &ISAPICameraDriver{deviceID: deviceID}
}

func (d *ISAPICameraDriver) ID() string                 { return "com.notrix.camera.isapi" }
func (d *ISAPICameraDriver) Version() string            { return "0.1.5" }
func (d *ISAPICameraDriver) Type() driversdk.DriverType { return driversdk.DriverTypeDevice }
func (d *ISAPICameraDriver) Protocols() []driversdk.Protocol {
	return []driversdk.Protocol{driversdk.ProtocolIP}
}
func (d *ISAPICameraDriver) Topologies() []driversdk.Topology {
	return []driversdk.Topology{driversdk.TopologyDirectIP}
}

func (d *ISAPICameraDriver) Init(ctx context.Context, deps driversdk.Dependencies, cfg driversdk.JSONConfig) error {
	d.deps = deps

	if err := cfg.Decode(&d.cfg); err != nil {
		return err
	}
	d.cfg.IP = strings.TrimSpace(d.cfg.IP)
	d.cfg.Username = strings.TrimSpace(d.cfg.Username)

	if d.cfg.IP == "" {
		return fmt.Errorf("config.ip is required")
	}
	if d.cfg.Username == "" {
		return fmt.Errorf("config.username is required")
	}
	if d.cfg.Password == "" {
		return fmt.Errorf("config.password is required")
	}
	if d.cfg.SnapshotPort == 0 {
		d.cfg.SnapshotPort = 80
	}
	if d.cfg.StreamPort == 0 {
		d.cfg.StreamPort = 554
	}
	if d.cfg.ChannelNumber <= 0 {
		d.cfg.ChannelNumber = 1
	}
	d.cfg.StreamType = strings.ToLower(strings.TrimSpace(d.cfg.StreamType))
	if d.cfg.StreamType == "" {
		d.cfg.StreamType = "sub"
	}
	if d.cfg.StreamType != "main" && d.cfg.StreamType != "sub" {
		return fmt.Errorf("config.stream_type must be 'main' or 'sub'")
	}
	if strings.TrimSpace(d.cfg.SnapshotResolution) == "" {
		d.cfg.SnapshotResolution = "640x480"
	}
	if d.cfg.RequestTimeoutSeconds <= 0 {
		d.cfg.RequestTimeoutSeconds = 8
	}
	if d.cfg.SnapshotRefreshMs <= 0 {
		d.cfg.SnapshotRefreshMs = 1000
	}
	if d.cfg.SnapshotRefreshMs < 250 {
		d.cfg.SnapshotRefreshMs = 250
	}
	if d.cfg.SnapshotRefreshMs > 10000 {
		d.cfg.SnapshotRefreshMs = 10000
	}

	d.client = NewClient(d.cfg)
	return nil
}

func (d *ISAPICameraDriver) Endpoints() ([]driversdk.Endpoint, error) {
	schema, _ := json.Marshal(map[string]any{"type": "object", "properties": map[string]any{}})

	return []driversdk.Endpoint{
		{
			Key:          "get_snapshot",
			Name:         "Get Snapshot",
			Direction:    driversdk.EndpointDirectionOutput,
			Kind:         driversdk.EndpointKindControl,
			Connection:   driversdk.EndpointConnectionIP,
			Icon:         "ip",
			MultiBinding: false,
			ControlType:  driversdk.ControlTypeButton,
			ValueSchema:  schema,
			Meta:         map[string]string{},
		},
		{
			Key:          "get_stream_url",
			Name:         "Get Stream URL",
			Direction:    driversdk.EndpointDirectionOutput,
			Kind:         driversdk.EndpointKindControl,
			Connection:   driversdk.EndpointConnectionIP,
			Icon:         "ip",
			MultiBinding: false,
			ControlType:  driversdk.ControlTypeButton,
			ValueSchema:  schema,
			Meta:         map[string]string{},
		},
	}, nil
}

func (d *ISAPICameraDriver) Variables() ([]driversdk.Variable, error) {
	return []driversdk.Variable{
		{Key: "snapshot", Type: driversdk.VariableTypeImage, Unit: "", Readable: true, Writable: false},
		{Key: "stream_url", Type: driversdk.VariableTypeVideo, Unit: "", Readable: true, Writable: false},
	}, nil
}

func (d *ISAPICameraDriver) Start(ctx context.Context) error {
	desc := driversdk.DeviceDescriptor{
		DeviceID:           d.deviceID,
		DriverID:           d.ID(),
		ExternalDeviceKey:  "camera:" + d.cfg.IP,
		DisplayName:        "IP Camera",
		DeviceType:         "camera",
		Manufacturer:       "",
		Model:              "",
		Firmware:           "",
		IPAddress:          d.cfg.IP,
		ConnectionCategory: "DIRECT_IP",
		Protocol:           "IP",
		Meta:               map[string]string{},
	}
	_ = d.deps.Publisher.UpsertDevice(ctx, desc)

	eps, _ := d.Endpoints()
	_ = d.deps.Publisher.UpsertEndpoints(ctx, d.deviceID, eps)

	vars, _ := d.Variables()
	_ = d.deps.Publisher.UpsertVariables(ctx, d.deviceID, vars)

	streamURL := d.client.StreamURL(d.cfg.EmbedCredentialsInRTSP)
	streamValue, _ := json.Marshal(streamURL)
	_ = d.deps.Publisher.PublishVariable(ctx, driversdk.VariableUpdate{
		DeviceID: d.deviceID,
		Key:      "stream_url",
		Value:    streamValue,
		Quality:  driversdk.QualityGood,
		Source:   driversdk.SourceDriver,
		At:       d.deps.Clock.Now(),
	})

	_ = d.publishSnapshot(ctx)

	d.stopCh = make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Duration(d.cfg.SnapshotRefreshMs) * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-d.stopCh:
				return
			case <-ticker.C:
				_ = d.publishSnapshot(ctx)
			}
		}
	}()

	return nil
}

func (d *ISAPICameraDriver) publishSnapshot(ctx context.Context) error {
	snapCtx, cancel := context.WithTimeout(ctx, time.Duration(d.cfg.RequestTimeoutSeconds)*time.Second)
	defer cancel()

	b, ct, err := d.client.FetchSnapshot(snapCtx)
	if err != nil {
		d.deps.Logger.Warn("snapshot fetch failed", "err", err.Error())
		return err
	}
	if len(b) == 0 {
		return nil
	}

	payload := map[string]any{
		"mime":         ct,
		"bytes_base64": base64.StdEncoding.EncodeToString(b),
		"snapshot_url": d.client.SnapshotURL(),
	}
	data, _ := json.Marshal(payload)
	_ = d.deps.Publisher.PublishVariable(ctx, driversdk.VariableUpdate{
		DeviceID: d.deviceID,
		Key:      "snapshot",
		Value:    data,
		Quality:  driversdk.QualityGood,
		Source:   driversdk.SourceDriver,
		At:       d.deps.Clock.Now(),
	})

	return nil
}

func (d *ISAPICameraDriver) HandleCommand(ctx context.Context, cmd driversdk.Command) (driversdk.CommandResult, error) {
	switch cmd.EndpointKey {
	case "get_snapshot":
		b, ct, err := d.client.FetchSnapshot(ctx)
		if err != nil {
			return driversdk.CommandResult{Success: false, Message: err.Error()}, err
		}

		payload := map[string]any{
			"mime":         ct,
			"bytes_base64": base64.StdEncoding.EncodeToString(b),
			"snapshot_url": d.client.SnapshotURL(),
		}
		data, _ := json.Marshal(payload)

		_ = d.deps.Publisher.PublishVariable(ctx, driversdk.VariableUpdate{
			DeviceID: d.deviceID,
			Key:      "snapshot",
			Value:    data,
			Quality:  driversdk.QualityGood,
			Source:   driversdk.SourceDriver,
			At:       d.deps.Clock.Now(),
		})

		return driversdk.CommandResult{Success: true, Message: "ok", Data: data}, nil

	case "get_stream_url":
		streamURL := d.client.StreamURL(false)
		out := map[string]any{
			"rtsp_url": streamURL,
		}
		if d.cfg.EmbedCredentialsInRTSP {
			out["rtsp_url_with_credentials"] = d.client.StreamURL(true)
		}
		data, _ := json.Marshal(out)
		return driversdk.CommandResult{Success: true, Message: "ok", Data: data}, nil

	default:
		return driversdk.CommandResult{Success: false, Message: "unknown endpoint"}, fmt.Errorf("unknown endpoint: %s", cmd.EndpointKey)
	}
}

func (d *ISAPICameraDriver) Health(ctx context.Context) (driversdk.HealthStatus, map[string]string) {
	return driversdk.HealthOK, map[string]string{"ip": d.cfg.IP}
}

func (d *ISAPICameraDriver) Stop(ctx context.Context) error {
	if d.stopCh != nil {
		close(d.stopCh)
		d.stopCh = nil
	}
	return nil
}
