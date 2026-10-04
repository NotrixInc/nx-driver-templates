package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	driversdk "github.com/NotrixInc/nx-driver-sdk"
)

// DescriptorMetric is where cap.camera@v1 publishes the camera's descriptor
// (CAMERA-STREAMING.md §4.1). The controller's Media Manager reads it and does
// all the video work; this driver only describes the camera.
const DescriptorMetric = "camera_descriptor"

// CameraCapType is the message type the controller sends camera commands as.
const CameraCapType = "cap.camera@v1"

// minProbeInterval is the floor for the reachability probe. Snapshots no
// longer travel the bus — the controller fetches them itself, on demand — so
// the probe only proves the camera answers, and once every ten seconds is
// plenty for that.
const minProbeInterval = 10 * time.Second

type ISAPICameraDriver struct {
	deviceID string
	deps     driversdk.Dependencies
	cfg      Config
	client   *Client
	stopCh   chan struct{}
	bus      *driversdk.MessageBus

	// stopMove ends a PTZ move that was never followed by ptz_stop — a phone
	// that drops off Wi-Fi mid-press must not leave the camera spinning.
	moveMu   sync.Mutex
	stopMove *time.Timer

	// Backoff tracking for snapshot failures.
	mu                  sync.Mutex
	consecutiveFailures int
	lastSnapshotOK      bool
}

func NewISAPICameraDriver(deviceID string) *ISAPICameraDriver {
	return &ISAPICameraDriver{deviceID: deviceID}
}

func (d *ISAPICameraDriver) ID() string                 { return "com.notrix.camera.isapi" }
func (d *ISAPICameraDriver) Version() string            { return "0.2.0" }
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
		{Key: DescriptorMetric, Type: driversdk.VariableTypeText, Unit: "", Readable: true, Writable: false},
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

	d.publishDescriptor(ctx)

	// Camera commands (PTZ, presets) arrive on this device's own queue. A
	// driver without the bus still serves video and snapshots; it just cannot
	// move the camera, so a failure here is logged rather than fatal.
	if bus, err := driversdk.NewMessageBusFromEnv(d.deviceID, d.deps.Logger); err != nil {
		d.deps.Logger.Warn("camera commands unavailable", "err", err.Error())
	} else {
		bus.RegisterHandlerFunc(CameraCapType, d.handleCameraMessage)
		if err := bus.Start(ctx); err != nil {
			d.deps.Logger.Warn("camera commands unavailable", "err", err.Error())
		} else {
			d.bus = bus
		}
	}

	// Don't block Start() with a potentially slow snapshot fetch —
	// let the background goroutine handle the first attempt too.

	d.stopCh = make(chan struct{})
	go func() {
		baseInterval := time.Duration(d.cfg.SnapshotRefreshMs) * time.Millisecond
		if baseInterval < minProbeInterval {
			baseInterval = minProbeInterval
		}
		currentInterval := baseInterval
		ticker := time.NewTicker(currentInterval)
		defer ticker.Stop()

		// Fetch immediately on goroutine start.
		d.doSnapshotWithBackoff(ctx, ticker, &currentInterval, baseInterval)

		for {
			select {
			case <-ctx.Done():
				return
			case <-d.stopCh:
				return
			case <-ticker.C:
				d.doSnapshotWithBackoff(ctx, ticker, &currentInterval, baseInterval)
			}
		}
	}()

	return nil
}

// doSnapshotWithBackoff fetches a snapshot and adjusts the ticker interval
// using exponential backoff on consecutive failures.
func (d *ISAPICameraDriver) doSnapshotWithBackoff(ctx context.Context, ticker *time.Ticker, current *time.Duration, base time.Duration) {
	err := d.publishSnapshot(ctx)

	d.mu.Lock()
	defer d.mu.Unlock()

	if err != nil {
		d.consecutiveFailures++
		d.lastSnapshotOK = false

		// Exponential backoff: double interval, capped at 60s.
		shift := d.consecutiveFailures
		if shift > 6 {
			shift = 6 // max 64× base
		}
		newInterval := base * time.Duration(1<<shift)
		maxInterval := 60 * time.Second
		if newInterval > maxInterval {
			newInterval = maxInterval
		}
		if newInterval != *current {
			*current = newInterval
			ticker.Reset(newInterval)
			d.deps.Logger.Info("snapshot backoff", "interval", newInterval.String(), "failures", d.consecutiveFailures)
		}
	} else {
		if d.consecutiveFailures > 0 {
			d.consecutiveFailures = 0
			*current = base
			ticker.Reset(base)
			d.deps.Logger.Info("snapshot recovered, reset interval", "interval", base.String())
		}
		d.lastSnapshotOK = true
	}
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

	// No JPEG bytes on the bus (CAMERA-STREAMING §12): the frame proved the
	// camera is reachable, and what is published points at the controller's own
	// snapshot endpoint, which serves it coalesced and resized.
	payload := map[string]any{
		"mime":         ct,
		"snapshot_url": "/v1/cameras/" + d.deviceID + "/snapshot",
		"captured_at":  d.deps.Clock.Now().UTC().Format(time.RFC3339),
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

		_ = b
		payload := map[string]any{
			"mime":         ct,
			"snapshot_url": "/v1/cameras/" + d.deviceID + "/snapshot",
			"captured_at":  d.deps.Clock.Now().UTC().Format(time.RFC3339),
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
	d.mu.Lock()
	failures := d.consecutiveFailures
	d.mu.Unlock()

	info := map[string]string{"ip": d.cfg.IP}
	if failures >= 5 {
		info["snapshot_status"] = "failing"
		info["consecutive_failures"] = fmt.Sprintf("%d", failures)
		return driversdk.HealthDegraded, info
	}
	if failures > 0 {
		info["snapshot_status"] = "retrying"
		info["consecutive_failures"] = fmt.Sprintf("%d", failures)
	}
	return driversdk.HealthOK, info
}

func (d *ISAPICameraDriver) Stop(ctx context.Context) error {
	d.disarmAutoStop()
	if d.bus != nil {
		_ = d.bus.Stop(ctx)
		d.bus = nil
	}
	if d.stopCh != nil {
		close(d.stopCh)
		d.stopCh = nil
	}
	return nil
}

// publishDescriptor tells the controller how to reach this camera's video.
// Credentials stay out of it: credential_ref names the controller's own copy
// of this device's username and password.
func (d *ISAPICameraDriver) publishDescriptor(ctx context.Context) {
	auth := d.client.AuthType()
	if auth != "basic" {
		auth = "digest"
	}
	desc := map[string]any{
		"camera_id": d.deviceID,
		"streams": map[string]any{
			"main": map[string]any{"url": d.client.MainStreamURL(), "codec": "h264"},
			"sub":  map[string]any{"url": d.client.SubStreamURL(), "codec": "h264"},
		},
		"snapshot":       map[string]any{"url": d.client.DescriptorSnapshotURL(), "auth": auth},
		"credential_ref": "secret://" + d.deviceID + "/camera",
		"caps": map[string]any{
			"ptz":           d.cfg.PTZ,
			"two_way_audio": false,
			"doorbell":      false,
			"motion_events": d.cfg.MotionEvents,
		},
	}
	data, _ := json.Marshal(desc)
	_ = d.deps.Publisher.PublishVariable(ctx, driversdk.VariableUpdate{
		DeviceID: d.deviceID,
		Key:      DescriptorMetric,
		Value:    data,
		Quality:  driversdk.QualityGood,
		Source:   driversdk.SourceDriver,
		At:       d.deps.Clock.Now(),
	})
}

// cameraCommand is the payload of a cap.camera@v1 message.
type cameraCommand struct {
	Command string         `json:"command"`
	Args    map[string]any `json:"args"`
}

// maxMove bounds a continuous move with no ptz_stop after it.
const maxMove = 2 * time.Second

func (d *ISAPICameraDriver) handleCameraMessage(ctx context.Context, msg driversdk.BusMessage) error {
	var cmd cameraCommand
	if err := json.Unmarshal(msg.Payload, &cmd); err != nil {
		return fmt.Errorf("bad %s payload: %w", CameraCapType, err)
	}
	if !d.cfg.PTZ {
		return fmt.Errorf("camera is not configured for PTZ")
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(d.cfg.RequestTimeoutSeconds)*time.Second)
	defer cancel()

	var err error
	switch cmd.Command {
	case "ptz_move":
		pan, tilt, zoom := num(cmd.Args["pan"]), num(cmd.Args["tilt"]), num(cmd.Args["zoom"])
		if err = d.client.PTZContinuous(cctx, pan, tilt, zoom); err == nil {
			d.armAutoStop()
		}
	case "ptz_stop":
		d.disarmAutoStop()
		err = d.client.PTZContinuous(cctx, 0, 0, 0)
	case "preset_goto":
		err = d.client.PresetGoto(cctx, int(num(cmd.Args["preset"])))
	case "preset_save":
		name, _ := cmd.Args["name"].(string)
		err = d.client.PresetSave(cctx, int(num(cmd.Args["preset"])), name)
	default:
		err = fmt.Errorf("unknown camera command %q", cmd.Command)
	}
	if err != nil {
		d.deps.Logger.Warn("camera command failed", "command", cmd.Command, "err", err.Error())
	}
	return err
}

func (d *ISAPICameraDriver) armAutoStop() {
	d.moveMu.Lock()
	defer d.moveMu.Unlock()
	if d.stopMove != nil {
		d.stopMove.Stop()
	}
	d.stopMove = time.AfterFunc(maxMove, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = d.client.PTZContinuous(ctx, 0, 0, 0)
	})
}

func (d *ISAPICameraDriver) disarmAutoStop() {
	d.moveMu.Lock()
	defer d.moveMu.Unlock()
	if d.stopMove != nil {
		d.stopMove.Stop()
		d.stopMove = nil
	}
}

func num(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	}
	return 0
}
