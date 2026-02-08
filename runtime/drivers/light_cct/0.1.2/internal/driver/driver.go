package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	driversdk "github.com/NotrixInc/nx-driver-sdk"
)

const (
	driverID      = "light_cct"
	driverVersion = "0.1.2"

	minCCT = 2700.0
	maxCCT = 6500.0
)

type LightCCTDriver struct {
	deviceID string

	deps driversdk.Dependencies
	cfg  Config

	mu              sync.Mutex
	lastCCT         float64
	lastBrightness  float64
	lastControlsTS  int64
	initializedOnce bool

	// MessageBus for driver-to-driver communication
	msgBus *driversdk.MessageBus

	wg       sync.WaitGroup
	stopOnce sync.Once
	stopCh   chan struct{}
}

func NewLightCCTDriver(deviceID string) *LightCCTDriver {
	return &LightCCTDriver{deviceID: deviceID, stopCh: make(chan struct{})}
}

func (d *LightCCTDriver) ID() string                 { return driverID }
func (d *LightCCTDriver) Version() string            { return driverVersion }
func (d *LightCCTDriver) Type() driversdk.DriverType { return driversdk.DriverTypeUI }
func (d *LightCCTDriver) Protocols() []driversdk.Protocol {
	return []driversdk.Protocol{}
}
func (d *LightCCTDriver) Topologies() []driversdk.Topology {
	return []driversdk.Topology{}
}

func clampCCT(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return minCCT
	}
	if v < minCCT {
		return minCCT
	}
	if v > maxCCT {
		return maxCCT
	}
	return v
}

func clamp01To100(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func (d *LightCCTDriver) Init(ctx context.Context, deps driversdk.Dependencies, cfg driversdk.JSONConfig) error {
	d.deps = deps
	_ = cfg.Decode(&d.cfg)

	initial := 3000.0
	if d.cfg.InitialCCT != nil {
		initial = *d.cfg.InitialCCT
	}
	d.lastCCT = clampCCT(initial)
	d.lastBrightness = 100
	return nil
}

func (d *LightCCTDriver) Endpoints() ([]driversdk.Endpoint, error) {
	valueSchema, _ := json.Marshal(map[string]any{"type": "number", "minimum": 0, "maximum": 100})
	return []driversdk.Endpoint{
		{
			Key:          "warm_white",
			Name:         "Warm White",
			Direction:    driversdk.EndpointDirectionOutput,
			Kind:         driversdk.EndpointKindControl,
			Connection:   driversdk.EndpointConnection("DC_Dimmer"),
			MultiBinding: true,
			ControlType:  "dimmer",
			ValueSchema:  valueSchema,
			Meta:         map[string]string{},
		},
		{
			Key:          "cool_white",
			Name:         "Cool White",
			Direction:    driversdk.EndpointDirectionOutput,
			Kind:         driversdk.EndpointKindControl,
			Connection:   driversdk.EndpointConnection("DC_Dimmer"),
			MultiBinding: true,
			ControlType:  "dimmer",
			ValueSchema:  valueSchema,
			Meta:         map[string]string{},
		},
	}, nil
}

func (d *LightCCTDriver) Variables() ([]driversdk.Variable, error) {
	return []driversdk.Variable{
		{Key: "brightness", Type: driversdk.VariableTypeNumber, Unit: "%", Readable: true, Writable: true},
		{Key: "power", Type: driversdk.VariableTypeBoolean, Unit: "", Readable: true, Writable: true},
		{Key: "cct", Type: driversdk.VariableTypeNumber, Unit: "K", Readable: true, Writable: true},
	}, nil
}

func (d *LightCCTDriver) Start(ctx context.Context) error {
	// Initialize the MessageBus for receiving scene messages
	var err error
	d.msgBus, err = driversdk.NewMessageBusFromEnv(driverID, d.deps.Logger)
	if err != nil {
		return fmt.Errorf("create message bus: %w", err)
	}

	// Register scene_apply handler
	d.msgBus.RegisterHandlerFunc("scene_apply", func(ctx context.Context, msg driversdk.BusMessage) error {
		// Controller-core scene messages are always authorized
		if msg.SourceDriver != "controller-core" {
			if d.deps.Logger != nil {
				d.deps.Logger.Debug("scene_apply from non-controller source rejected", "source", msg.SourceDriver)
			}
			return nil
		}
		return d.handleSceneApplyMessage(msg)
	})

	// Start the message bus
	if err := d.msgBus.Start(ctx); err != nil {
		return fmt.Errorf("start message bus: %w", err)
	}

	d.wg.Add(1)
	go d.controlLoop()
	return nil
}

func (d *LightCCTDriver) Stop(ctx context.Context) error {
	d.stopOnce.Do(func() { close(d.stopCh) })

	// Stop the message bus
	if d.msgBus != nil {
		_ = d.msgBus.Stop(ctx)
	}

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

func (d *LightCCTDriver) controlLoop() {
	defer d.wg.Done()

	t := time.NewTicker(300 * time.Millisecond)
	defer t.Stop()

	for {
		select {
		case <-d.stopCh:
			return
		case <-t.C:
			_ = d.applyDesiredCCT()
		}
	}
}

func (d *LightCCTDriver) applyDesiredCCT() error {
	_, controls, controlsTS, err := d.readControlsFromCore()
	if err != nil || controls == nil {
		return err
	}

	d.mu.Lock()
	lastTS := d.lastControlsTS
	lastCCT := d.lastCCT
	d.mu.Unlock()

	if controlsTS > 0 && controlsTS <= lastTS {
		return nil
	}

	var (
		hasCCT        bool
		hasBrightness bool
		hasPower      bool
		cctVal        float64
		brightnessVal float64
		powerVal      bool
	)

	if v, ok := controls["cct"]; ok {
		if n, ok := coerceNumber(v); ok {
			cctVal = clampCCT(n)
			hasCCT = true
		}
	} else if v, okAlt := controls["CCT"]; okAlt {
		if n, ok := coerceNumber(v); ok {
			cctVal = clampCCT(n)
			hasCCT = true
		}
	}

	if v, ok := controls["brightness"]; ok {
		if n, ok := coerceNumber(v); ok {
			brightnessVal = clamp01To100(n)
			hasBrightness = true
		}
	}

	if v, ok := controls["power"]; ok {
		switch t := v.(type) {
		case bool:
			powerVal = t
			hasPower = true
		case string:
			powerVal = strings.TrimSpace(strings.ToLower(t)) == "true"
			hasPower = true
		}
	}

	if !hasCCT && !hasBrightness && !hasPower {
		return nil
	}

	if !hasCCT {
		cctVal = lastCCT
	}
	if !hasBrightness {
		brightnessVal = d.lastBrightness
	}
	if hasPower && !powerVal {
		brightnessVal = 0
	}
	if hasPower && powerVal && brightnessVal == 0 {
		brightnessVal = d.lastBrightness
	}
	brightnessVal = clamp01To100(brightnessVal)

	if d.initializedOnce && math.Abs(cctVal-lastCCT) < 0.5 && math.Abs(brightnessVal-d.lastBrightness) < 0.5 {
		return nil
	}

	baseWarm, baseCool := computeWarmCool(cctVal)
	scale := brightnessVal / 100.0
	warmPct := math.Round(baseWarm * scale)
	coolPct := math.Round(baseCool * scale)

	if warmPct < 0 {
		warmPct = 0
	}
	if warmPct > 100 {
		warmPct = 100
	}
	if coolPct < 0 {
		coolPct = 0
	}
	if coolPct > 100 {
		coolPct = 100
	}

	// Update device meta controls so controller-core can propagate bindings
	nextControls := map[string]any{}
	for k, v := range controls {
		nextControls[k] = v
	}
	nextControls["cct"] = cctVal
	nextControls["brightness"] = brightnessVal
	nextControls["power"] = brightnessVal > 0
	nextControls["warm_white"] = warmPct
	nextControls["cool_white"] = coolPct

	if err := d.setDesiredControlsInCore(nextControls, "USER"); err != nil {
		return err
	}

	// Publish variables for UI visibility (best-effort)
	_ = d.publishCCT(cctVal)
	_ = d.publishBrightness(brightnessVal)
	_ = d.publishPower(brightnessVal > 0)

	d.mu.Lock()
	d.lastCCT = cctVal
	d.lastBrightness = brightnessVal
	d.lastControlsTS = time.Now().UnixMilli()
	d.initializedOnce = true
	d.mu.Unlock()

	return nil
}

// SceneApplyPayload is the payload for scene_apply messages from controller-core
type SceneApplyPayload struct {
	DeviceID   string   `json:"device_id"`
	SceneID    string   `json:"scene_id,omitempty"`
	Brightness *float64 `json:"brightness,omitempty"`
	CCT        *float64 `json:"cct,omitempty"`
	On         *bool    `json:"on,omitempty"`
	Power      *bool    `json:"power,omitempty"`
}

// handleSceneApplyMessage processes scene_apply messages from controller-core
func (d *LightCCTDriver) handleSceneApplyMessage(msg driversdk.BusMessage) error {
	var payload SceneApplyPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		if d.deps.Logger != nil {
			d.deps.Logger.Error("failed to parse scene_apply payload", "error", err)
		}
		return err
	}

	// Check if this message is for our device
	if payload.DeviceID != "" && !strings.EqualFold(strings.TrimSpace(payload.DeviceID), strings.TrimSpace(d.deviceID)) {
		return nil
	}

	if d.deps.Logger != nil {
		d.deps.Logger.Info("applying scene", "scene_id", msg.CorrelationID, "device_id", payload.DeviceID, "brightness", payload.Brightness, "cct", payload.CCT, "on", payload.On)
	}

	// Determine power state
	powerOn := true
	if payload.On != nil {
		powerOn = *payload.On
	} else if payload.Power != nil {
		powerOn = *payload.Power
	}

	// Build controls map
	controls := map[string]any{}

	// Get current values as defaults
	d.mu.Lock()
	cctVal := d.lastCCT
	brightnessVal := d.lastBrightness
	d.mu.Unlock()

	// Apply CCT if provided
	if payload.CCT != nil {
		cctVal = clampCCT(*payload.CCT)
	}

	// Apply brightness if provided
	if payload.Brightness != nil {
		brightnessVal = clamp01To100(*payload.Brightness)
	}

	// If power is off, set brightness to 0
	if !powerOn {
		brightnessVal = 0
	}

	// Compute warm/cool values
	baseWarm, baseCool := computeWarmCool(cctVal)
	scale := brightnessVal / 100.0
	warmPct := math.Round(baseWarm * scale)
	coolPct := math.Round(baseCool * scale)

	if warmPct < 0 {
		warmPct = 0
	}
	if warmPct > 100 {
		warmPct = 100
	}
	if coolPct < 0 {
		coolPct = 0
	}
	if coolPct > 100 {
		coolPct = 100
	}

	controls["cct"] = cctVal
	controls["brightness"] = brightnessVal
	controls["power"] = brightnessVal > 0
	controls["warm_white"] = warmPct
	controls["cool_white"] = coolPct

	// Update device meta controls
	if err := d.setDesiredControlsInCore(controls, "SCENE"); err != nil {
		return err
	}

	// Publish variables for UI visibility
	_ = d.publishCCT(cctVal)
	_ = d.publishBrightness(brightnessVal)
	_ = d.publishPower(brightnessVal > 0)

	d.mu.Lock()
	d.lastCCT = cctVal
	d.lastBrightness = brightnessVal
	d.lastControlsTS = time.Now().UnixMilli()
	d.mu.Unlock()

	return nil
}

func computeWarmCool(cct float64) (warmPct float64, coolPct float64) {
	pos := (cct - minCCT) / (maxCCT - minCCT)
	if pos < 0 {
		pos = 0
	}
	if pos > 1 {
		pos = 1
	}
	coolPct = math.Round(pos * 100)
	warmPct = 100 - coolPct
	return warmPct, coolPct
}

func (d *LightCCTDriver) publishCCT(cct float64) error {
	if d.deps.Publisher == nil {
		return nil
	}
	b, _ := json.Marshal(cct)
	return d.deps.Publisher.PublishVariable(context.Background(), driversdk.VariableUpdate{
		DeviceID: d.deviceID,
		Key:      "cct",
		Value:    b,
		Quality:  driversdk.QualityGood,
		Source:   driversdk.SourceDriver,
		At:       time.Now(),
	})
}

func (d *LightCCTDriver) publishBrightness(v float64) error {
	if d.deps.Publisher == nil {
		return nil
	}
	b, _ := json.Marshal(v)
	return d.deps.Publisher.PublishVariable(context.Background(), driversdk.VariableUpdate{
		DeviceID: d.deviceID,
		Key:      "brightness",
		Value:    b,
		Quality:  driversdk.QualityGood,
		Source:   driversdk.SourceDriver,
		At:       time.Now(),
	})
}

func (d *LightCCTDriver) publishPower(v bool) error {
	if d.deps.Publisher == nil {
		return nil
	}
	b, _ := json.Marshal(v)
	return d.deps.Publisher.PublishVariable(context.Background(), driversdk.VariableUpdate{
		DeviceID: d.deviceID,
		Key:      "power",
		Value:    b,
		Quality:  driversdk.QualityGood,
		Source:   driversdk.SourceDriver,
		At:       time.Now(),
	})
}

func (d *LightCCTDriver) coreHTTPAddr() string {
	coreHTTP := strings.TrimSpace(os.Getenv("CORE_HTTP_ADDR"))
	if coreHTTP == "" {
		coreHTTP = strings.TrimSpace(os.Getenv("CONTROLLER_CORE_HTTP_ADDR"))
	}
	if coreHTTP == "" {
		coreHTTP = "http://127.0.0.1:8090"
	}
	return strings.TrimRight(coreHTTP, "/")
}

func (d *LightCCTDriver) readControlsFromCore() (map[string]any, map[string]any, int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v1/devices?id=%s", d.coreHTTPAddr(), d.deviceID), nil)
	if err != nil {
		return nil, nil, 0, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.ReadAll(resp.Body)
		return nil, nil, 0, fmt.Errorf("core http get device: status %s", resp.Status)
	}

	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, nil, 0, err
	}

	metaAny, _ := raw["meta_json"].(map[string]any)
	if metaAny == nil {
		metaAny, _ = raw["MetaJson"].(map[string]any)
	}
	if metaAny == nil {
		return nil, nil, 0, nil
	}

	controlsAny, _ := metaAny["controls"].(map[string]any)
	if controlsAny == nil {
		return metaAny, nil, 0, nil
	}

	var ts int64
	if v, ok := metaAny["controls_ts_unix_ms"]; ok {
		if n, ok := coerceNumber(v); ok {
			ts = int64(n)
		}
	}

	return metaAny, controlsAny, ts, nil
}

func (d *LightCCTDriver) setDesiredControlsInCore(next map[string]any, controlsSource string) error {
	body, _ := json.Marshal(map[string]any{
		"id": d.deviceID,
		"meta": map[string]any{
			"controls":            next,
			"controls_source":     strings.TrimSpace(controlsSource),
			"controls_ts_unix_ms": time.Now().UnixMilli(),
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, d.coreHTTPAddr()+"/v1/devices", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("core http set controls: status %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

func coerceNumber(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case int32:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}
