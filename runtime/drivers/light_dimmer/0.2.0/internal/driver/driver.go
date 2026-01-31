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
	driverID      = "light_dimmer"
	driverVersion = "0.2.0"
)

type LightDimmerDriver struct {
	deviceID string

	deps driversdk.Dependencies
	cfg  Config

	mu                    sync.Mutex
	brightness            float64
	lastNonZeroBrightness float64

	// MessageBus for driver-to-driver communication with authorization
	msgBus *driversdk.MessageBus

	bindingsMu         sync.Mutex
	bindingsAt         time.Time
	bindingsByEndpoint map[string][]bindingTarget

	// Driver-to-driver bindings for authorization
	driverBindingsMu sync.Mutex
	driverBindings   []driverToDriverBinding

	wg sync.WaitGroup

	stopOnce sync.Once
	stopCh   chan struct{}
}

type bindingTarget struct {
	TargetDeviceID   string
	TargetEndpointID string
}

type coreBinding struct {
	BindingType      string  `json:"binding_type"`
	Enabled          *bool   `json:"enabled"`
	SourceDeviceID   *string `json:"source_device_id"`
	SourceEndpointID *string `json:"source_endpoint_id"`
	TargetDeviceID   *string `json:"target_device_id"`
	TargetEndpointID *string `json:"target_endpoint_id"`
	MetaJSON         *struct {
		SourceDriverID string `json:"source_driver_id"`
		TargetDriverID string `json:"target_driver_id"`
		Bidirectional  bool   `json:"bidirectional"`
	} `json:"meta_json,omitempty"`
}

type driverToDriverBinding struct {
	SourceDriverID string
	TargetDriverID string
	Bidirectional  bool
	Enabled        bool
}

func NewLightDimmerDriver(deviceID string) *LightDimmerDriver {
	return &LightDimmerDriver{deviceID: deviceID, stopCh: make(chan struct{})}
}

func (d *LightDimmerDriver) ID() string                 { return driverID }
func (d *LightDimmerDriver) Version() string            { return driverVersion }
func (d *LightDimmerDriver) Type() driversdk.DriverType { return driversdk.DriverTypeDevice }
func (d *LightDimmerDriver) Protocols() []driversdk.Protocol {
	return []driversdk.Protocol{driversdk.ProtocolIP}
}
func (d *LightDimmerDriver) Topologies() []driversdk.Topology {
	return []driversdk.Topology{driversdk.TopologyDirectIP}
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

func (d *LightDimmerDriver) Init(ctx context.Context, deps driversdk.Dependencies, cfg driversdk.JSONConfig) error {
	d.deps = deps
	_ = cfg.Decode(&d.cfg) // config is optional

	initial := 100.0
	if d.cfg.InitialBrightness != nil {
		initial = *d.cfg.InitialBrightness
	}
	d.brightness = clamp01To100(initial)
	d.lastNonZeroBrightness = d.brightness
	return nil
}

func (d *LightDimmerDriver) Endpoints() ([]driversdk.Endpoint, error) {
	valueSchema, _ := json.Marshal(map[string]any{"type": "number", "minimum": 0, "maximum": 100})
	boolSchema, _ := json.Marshal(map[string]any{"type": "boolean"})
	return []driversdk.Endpoint{
		{
			Key:          "brightness",
			Name:         "Brightness",
			Direction:    driversdk.EndpointDirectionOutput,
			Kind:         driversdk.EndpointKindControl,
			Connection:   driversdk.EndpointConnectionIP,
			Icon:         "light",
			MultiBinding: true,
			ControlType:  "dimmer",
			ValueSchema:  valueSchema,
			Meta:         map[string]string{},
		},
		{
			Key:          "power",
			Name:         "Power",
			Direction:    driversdk.EndpointDirectionOutput,
			Kind:         driversdk.EndpointKindControl,
			Connection:   driversdk.EndpointConnectionIP,
			Icon:         "power",
			MultiBinding: true,
			ControlType:  "on-off",
			ValueSchema:  boolSchema,
			Meta:         map[string]string{},
		},
	}, nil
}

func (d *LightDimmerDriver) Variables() ([]driversdk.Variable, error) {
	return []driversdk.Variable{
		{Key: "brightness", Type: driversdk.VariableTypeNumber, Unit: "%", Readable: true, Writable: true},
		{Key: "power", Type: driversdk.VariableTypeBoolean, Unit: "", Readable: true, Writable: true},
	}, nil
}

func (d *LightDimmerDriver) Start(ctx context.Context) error {
	// Publish initial state so UI has a value.
	if err := d.publishBrightness(ctx, d.currentBrightness(), driversdk.SourceDriver); err != nil {
		return err
	}

	// Initialize the MessageBus for driver-to-driver communication
	// Note: MessageBus is a simple transport - authorization is handled by the driver
	var err error
	d.msgBus, err = driversdk.NewMessageBusFromEnv(driverID, d.deps.Logger)
	if err != nil {
		return fmt.Errorf("create message bus: %w", err)
	}

	// Register message handlers - the driver handles authorization using its own bindings
	d.registerMessageHandlers()

	// Start the message bus (handles polling and handler dispatch internally)
	if err := d.msgBus.Start(ctx); err != nil {
		return fmt.Errorf("start message bus: %w", err)
	}

	// Poll desired controls from controller-core and emit telemetry.
	// This is the simplest way to integrate with the current platform flow
	// (UI writes device.meta_json.controls; drivers apply changes).
	d.wg.Add(1)
	go d.controlLoop()

	return nil
}

// registerMessageHandlers sets up message handlers
// Authorization is done by the driver using its own binding information
func (d *LightDimmerDriver) registerMessageHandlers() {
	// Handler for dimming.changed messages
	d.msgBus.RegisterHandlerFunc("dimming.changed", func(ctx context.Context, msg driversdk.BusMessage) error {
		// Authorize: check if source driver is bound to us
		if !d.isAuthorizedSource(msg.SourceDriver) {
			if d.deps.Logger != nil {
				d.deps.Logger.Debug("unauthorized message rejected", "source", msg.SourceDriver, "type", msg.Type)
			}
			return nil
		}

		payload, err := driversdk.ParsePayload[driversdk.DimmingPayload](msg.Payload)
		if err != nil {
			return err
		}
		return d.handleDimmingMessage(msg, payload)
	})

	// Handler for dimming.set messages
	d.msgBus.RegisterHandlerFunc("dimming.set", func(ctx context.Context, msg driversdk.BusMessage) error {
		// Authorize: check if source driver is bound to us
		if !d.isAuthorizedSource(msg.SourceDriver) {
			if d.deps.Logger != nil {
				d.deps.Logger.Debug("unauthorized message rejected", "source", msg.SourceDriver, "type", msg.Type)
			}
			return nil
		}

		payload, err := driversdk.ParsePayload[driversdk.DimmingPayload](msg.Payload)
		if err != nil {
			return err
		}
		return d.handleDimmingMessage(msg, payload)
	})

	// Handler for power messages
	d.msgBus.RegisterHandlerFunc("power.set", func(ctx context.Context, msg driversdk.BusMessage) error {
		// Authorize: check if source driver is bound to us
		if !d.isAuthorizedSource(msg.SourceDriver) {
			if d.deps.Logger != nil {
				d.deps.Logger.Debug("unauthorized message rejected", "source", msg.SourceDriver, "type", msg.Type)
			}
			return nil
		}

		payload, err := driversdk.ParsePayload[driversdk.PowerPayload](msg.Payload)
		if err != nil {
			return err
		}
		return d.handlePowerMessage(msg, payload)
	})
}

// isAuthorizedSource checks if the source driver is authorized to send messages to us
// This uses the driver's own binding cache for authorization
func (d *LightDimmerDriver) isAuthorizedSource(sourceDriverID string) bool {
	sourceDriverID = strings.TrimSpace(sourceDriverID)
	if sourceDriverID == "" {
		return false
	}

	// Get bindings where we are the target (receiving messages)
	d.bindingsMu.Lock()
	stale := d.bindingsByEndpoint == nil || time.Since(d.bindingsAt) > 2*time.Second
	d.bindingsMu.Unlock()
	if stale {
		d.refreshBindingCache()
	}

	// Check driver-to-driver bindings
	bindings := d.getDriverToDriverBindings()
	for _, b := range bindings {
		// Source can send to us if there's a binding from source to us
		if strings.EqualFold(strings.TrimSpace(b.SourceDriverID), sourceDriverID) &&
			strings.EqualFold(strings.TrimSpace(b.TargetDriverID), driverID) {
			return true
		}
		// Or if bidirectional binding exists in either direction
		if b.Bidirectional {
			if strings.EqualFold(strings.TrimSpace(b.TargetDriverID), sourceDriverID) &&
				strings.EqualFold(strings.TrimSpace(b.SourceDriverID), driverID) {
				return true
			}
		}
	}

	return false
}

// getBoundDriverIDs returns all driver IDs we are bound to (can send messages to)
func (d *LightDimmerDriver) getBoundDriverIDs() []string {
	bindings := d.getDriverToDriverBindings()
	driverSet := make(map[string]struct{})

	for _, b := range bindings {
		srcID := strings.TrimSpace(strings.ToLower(b.SourceDriverID))
		tgtID := strings.TrimSpace(strings.ToLower(b.TargetDriverID))
		selfID := strings.ToLower(driverID)

		// We can send to target if we are the source
		if srcID == selfID {
			driverSet[b.TargetDriverID] = struct{}{}
		}
		// We can send to source if bidirectional and we are the target
		if b.Bidirectional && tgtID == selfID {
			driverSet[b.SourceDriverID] = struct{}{}
		}
	}

	drivers := make([]string, 0, len(driverSet))
	for d := range driverSet {
		drivers = append(drivers, d)
	}
	return drivers
}

// handleDimmingMessage processes authorized dimming messages
func (d *LightDimmerDriver) handleDimmingMessage(msg driversdk.BusMessage, payload *driversdk.DimmingPayload) error {
	// Drop stale messages
	if msg.TSUnixMs > 0 {
		age := time.Now().UnixMilli() - msg.TSUnixMs
		if age > 30_000 {
			return nil
		}
	}

	// Check if this message is targeted at our device
	if payload.TargetDeviceID != "" && !strings.EqualFold(strings.TrimSpace(payload.TargetDeviceID), strings.TrimSpace(d.deviceID)) {
		return nil
	}

	val := payload.GetBrightness()
	val = clamp01To100(val)

	d.mu.Lock()
	d.brightness = val
	if val > 0.5 {
		d.lastNonZeroBrightness = val
	}
	d.mu.Unlock()

	_ = d.setDesiredControlInCore(val, "BUS")
	_ = d.publishBrightness(context.Background(), val, driversdk.SourceDriver)
	return nil
}

// handlePowerMessage processes authorized power messages
func (d *LightDimmerDriver) handlePowerMessage(msg driversdk.BusMessage, payload *driversdk.PowerPayload) error {
	// Check if this message is targeted at our device
	if payload.TargetDeviceID != "" && !strings.EqualFold(strings.TrimSpace(payload.TargetDeviceID), strings.TrimSpace(d.deviceID)) {
		return nil
	}

	if !payload.IsPowerOn() {
		d.mu.Lock()
		d.brightness = 0
		d.mu.Unlock()
		_ = d.setDesiredControlsInCore(map[string]any{"brightness": 0, "power": false}, "BUS")
		_ = d.publishBrightness(context.Background(), 0, driversdk.SourceDriver)
		return nil
	}

	// Power on: restore last non-zero or default to 100
	d.mu.Lock()
	restore := d.lastNonZeroBrightness
	if restore <= 0.5 {
		restore = 100
	}
	d.brightness = restore
	d.mu.Unlock()

	_ = d.setDesiredControlsInCore(map[string]any{"brightness": restore, "power": true}, "BUS")
	_ = d.publishBrightness(context.Background(), restore, driversdk.SourceDriver)
	return nil
}

func (d *LightDimmerDriver) listBindings(ctx context.Context) ([]coreBinding, error) {
	coreHTTP := strings.TrimSpace(os.Getenv("CORE_HTTP_ADDR"))
	if coreHTTP == "" {
		coreHTTP = strings.TrimSpace(os.Getenv("CONTROLLER_CORE_HTTP_ADDR"))
	}
	if coreHTTP == "" {
		coreHTTP = "http://127.0.0.1:8090"
	}
	coreHTTP = strings.TrimRight(coreHTTP, "/")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, coreHTTP+"/v1/bindings", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("core http list bindings: status %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var out []coreBinding
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func (d *LightDimmerDriver) refreshBindingCache() {
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	rows, err := d.listBindings(ctx)
	cancel()
	if err != nil {
		return
	}

	byEp := map[string][]bindingTarget{}
	for _, b := range rows {
		typeRaw := strings.TrimSpace(b.BindingType)
		if typeRaw != "ENDPOINT_TO_ENDPOINT" && typeRaw != "DEVICE_ENDPOINT" {
			continue
		}
		if b.Enabled != nil && !*b.Enabled {
			continue
		}
		srcDev := strings.TrimSpace(derefString(b.SourceDeviceID))
		if srcDev == "" || !strings.EqualFold(srcDev, strings.TrimSpace(d.deviceID)) {
			continue
		}
		srcEp := strings.TrimSpace(derefString(b.SourceEndpointID))
		if srcEp == "" {
			continue
		}
		tgtDev := strings.TrimSpace(derefString(b.TargetDeviceID))
		if tgtDev == "" {
			continue
		}
		tgtEp := strings.TrimSpace(derefString(b.TargetEndpointID))
		byEp[srcEp] = append(byEp[srcEp], bindingTarget{TargetDeviceID: tgtDev, TargetEndpointID: tgtEp})
	}

	// Also extract driver-to-driver bindings for message authorization
	driverBindings := []driverToDriverBinding{}
	for _, b := range rows {
		if strings.TrimSpace(b.BindingType) != "DRIVER_TO_DRIVER" {
			continue
		}
		enabled := b.Enabled == nil || *b.Enabled
		if b.MetaJSON != nil {
			driverBindings = append(driverBindings, driverToDriverBinding{
				SourceDriverID: b.MetaJSON.SourceDriverID,
				TargetDriverID: b.MetaJSON.TargetDriverID,
				Bidirectional:  b.MetaJSON.Bidirectional,
				Enabled:        enabled,
			})
		}
	}

	d.bindingsMu.Lock()
	d.bindingsByEndpoint = byEp
	d.bindingsAt = time.Now()
	d.bindingsMu.Unlock()

	d.driverBindingsMu.Lock()
	d.driverBindings = driverBindings
	d.driverBindingsMu.Unlock()
}

// getDriverToDriverBindings returns driver-to-driver bindings for message authorization
func (d *LightDimmerDriver) getDriverToDriverBindings() []driverToDriverBinding {
	d.driverBindingsMu.Lock()
	defer d.driverBindingsMu.Unlock()

	// Copy to avoid caller mutating
	out := make([]driverToDriverBinding, len(d.driverBindings))
	copy(out, d.driverBindings)
	return out
}

func (d *LightDimmerDriver) getBindingTargets(endpointKey string) []bindingTarget {
	endpointKey = strings.TrimSpace(endpointKey)
	if endpointKey == "" {
		return nil
	}

	d.bindingsMu.Lock()
	stale := d.bindingsByEndpoint == nil || time.Since(d.bindingsAt) > 2*time.Second
	d.bindingsMu.Unlock()
	if stale {
		d.refreshBindingCache()
	}

	d.bindingsMu.Lock()
	defer d.bindingsMu.Unlock()
	if d.bindingsByEndpoint == nil {
		return nil
	}
	// Copy to avoid caller mutating.
	items := d.bindingsByEndpoint[endpointKey]
	out := make([]bindingTarget, 0, len(items))
	for _, it := range items {
		out = append(out, it)
	}
	return out
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (d *LightDimmerDriver) setDesiredControlInCore(brightness float64, controlsSource string) error {
	return d.setDesiredControlsInCore(map[string]any{"brightness": brightness, "power": brightness > 0.5}, controlsSource)
}

func (d *LightDimmerDriver) setDesiredControlsInCore(next map[string]any, controlsSource string) error {
	coreHTTP := strings.TrimSpace(os.Getenv("CORE_HTTP_ADDR"))
	if coreHTTP == "" {
		coreHTTP = strings.TrimSpace(os.Getenv("CONTROLLER_CORE_HTTP_ADDR"))
	}
	if coreHTTP == "" {
		coreHTTP = "http://127.0.0.1:8090"
	}
	coreHTTP = strings.TrimRight(coreHTTP, "/")

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

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, coreHTTP+"/v1/devices", bytes.NewReader(body))
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

type coreDeviceResponse struct {
	MetaJSON map[string]any `json:"meta_json"`
}

func (d *LightDimmerDriver) controlLoop() {
	defer d.wg.Done()

	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()

	for {
		select {
		case <-d.stopCh:
			return
		case <-t.C:
			_ = d.applyDesiredControl()
		}
	}
}

func (d *LightDimmerDriver) applyDesiredControl() error {
	coreHTTP := strings.TrimSpace(os.Getenv("CORE_HTTP_ADDR"))
	if coreHTTP == "" {
		coreHTTP = strings.TrimSpace(os.Getenv("CONTROLLER_CORE_HTTP_ADDR"))
	}
	if coreHTTP == "" {
		coreHTTP = "http://127.0.0.1:8090"
	}
	coreHTTP = strings.TrimRight(coreHTTP, "/")

	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v1/devices?id=%s", coreHTTP, d.deviceID), nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.ReadAll(resp.Body)
		return fmt.Errorf("core http get device: status %s", resp.Status)
	}

	var dev coreDeviceResponse
	if err := json.NewDecoder(resp.Body).Decode(&dev); err != nil {
		return err
	}

	controlsAny, ok := dev.MetaJSON["controls"].(map[string]any)
	if !ok || controlsAny == nil {
		controlsAny, _ = dev.MetaJSON["variables"].(map[string]any)
	}
	if controlsAny == nil {
		return nil
	}

	// If power is explicitly set, honor it.
	if pAny, ok := controlsAny["power"]; ok {
		if p, ok := coerceBool(pAny); ok {
			if !p {
				cur := d.currentBrightness()
				if cur > 0.5 {
					d.mu.Lock()
					d.lastNonZeroBrightness = cur
					d.mu.Unlock()
				}
				d.mu.Lock()
				d.brightness = 0
				d.mu.Unlock()
				return d.publishBrightness(context.Background(), 0, driversdk.SourceDriver)
			}
			// If power on and brightness isn't explicitly set, restore last non-zero.
			if _, hasBrightness := controlsAny["brightness"]; !hasBrightness {
				d.mu.Lock()
				restore := d.lastNonZeroBrightness
				if restore <= 0.5 {
					restore = 100
				}
				d.brightness = restore
				d.mu.Unlock()
				return d.publishBrightness(context.Background(), restore, driversdk.SourceDriver)
			}
		}
	}

	desiredAny, ok := controlsAny["brightness"]
	if !ok {
		return nil
	}

	desired, ok := coerceNumber(desiredAny)
	if !ok {
		return nil
	}
	desired = clamp01To100(desired)

	cur := d.currentBrightness()
	if math.Abs(cur-desired) < 0.5 {
		return nil
	}

	d.mu.Lock()
	d.brightness = desired
	if desired > 0.5 {
		d.lastNonZeroBrightness = desired
	}
	d.mu.Unlock()

	// If controller-core indicates this control did not come from a user action,
	// treat it as non-user input to avoid broadcasting it back to other drivers
	// (feedback loops between bindings/bus/controls).
	src := driversdk.SourceUser
	if s, ok := dev.MetaJSON["controls_source"].(string); ok {
		s = strings.TrimSpace(s)
		if s != "" && !strings.EqualFold(s, "USER") {
			src = driversdk.SourceDriver
		}
	}

	return d.publishBrightness(context.Background(), desired, src)
}

func coerceBool(v any) (bool, bool) {
	switch t := v.(type) {
	case bool:
		return t, true
	case string:
		s := strings.TrimSpace(strings.ToLower(t))
		if s == "true" || s == "1" || s == "on" || s == "yes" {
			return true, true
		}
		if s == "false" || s == "0" || s == "off" || s == "no" {
			return false, true
		}
		return false, false
	default:
		return false, false
	}
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
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func (d *LightDimmerDriver) currentBrightness() float64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.brightness
}

func (d *LightDimmerDriver) publishBrightness(ctx context.Context, v float64, source driversdk.Source) error {
	power := v > 0.5
	state, _ := json.Marshal(map[string]any{"brightness": v})
	_ = d.deps.Publisher.PublishState(ctx, driversdk.StateUpdate{
		DeviceID: d.deviceID,
		State:    state,
		At:       d.deps.Clock.Now(),
	})

	payload, _ := json.Marshal(map[string]any{"brightness": v, "source": string(source)})
	_ = d.deps.Publisher.PublishEvent(ctx, driversdk.DeviceEvent{
		DeviceID: d.deviceID,
		Type:     "BRIGHTNESS_CHANGED",
		Payload:  payload,
		Severity: driversdk.EventInfo,
		At:       d.deps.Clock.Now(),
	})

	valueJSON, _ := json.Marshal(v)
	_ = d.deps.Publisher.PublishVariable(ctx, driversdk.VariableUpdate{
		DeviceID: d.deviceID,
		Key:      "brightness",
		Value:    json.RawMessage(valueJSON),
		Quality:  driversdk.QualityGood,
		Source:   source,
		At:       d.deps.Clock.Now(),
	})

	powerJSON, _ := json.Marshal(power)
	_ = d.deps.Publisher.PublishVariable(ctx, driversdk.VariableUpdate{
		DeviceID: d.deviceID,
		Key:      "power",
		Value:    json.RawMessage(powerJSON),
		Quality:  driversdk.QualityGood,
		Source:   source,
		At:       d.deps.Clock.Now(),
	})

	// Broadcast only user-driven changes to bound drivers using MessageBus
	// The driver determines bound drivers from its own binding cache
	if source == driversdk.SourceUser && d.msgBus != nil {
		// Get all drivers we're bound to from our own binding info
		boundDrivers := d.getBoundDriverIDs()
		if len(boundDrivers) > 0 {
			msgPayload := &driversdk.DimmingPayload{
				EndpointKey:       "brightness",
				SourceEndpointKey: "brightness",
				Value:             v,
				Brightness:        v,
				Power:             power,
				DeviceID:          d.deviceID,
				SourceDeviceID:    d.deviceID,
			}

			// Use multicast to send to all bound drivers at once
			msgCtx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
			_, err := d.msgBus.Multicast(msgCtx, boundDrivers, "dimming.changed", msgPayload,
				driversdk.WithTTL(30000),
				driversdk.WithPriority(driversdk.PriorityNormal),
			)
			cancel()
			if err != nil && d.deps.Logger != nil {
				d.deps.Logger.Debug("message bus multicast failed", "err", err.Error())
			}
		}
	}

	return nil
}

// SendDimmingWithAck sends a dimming command to a specific driver and waits for acknowledgment
// This is useful when you need to confirm the command was received and processed
func (d *LightDimmerDriver) SendDimmingWithAck(ctx context.Context, targetDriverID string, brightness float64) (*driversdk.AckResult, error) {
	if d.msgBus == nil {
		return nil, fmt.Errorf("message bus not initialized")
	}

	payload := &driversdk.DimmingPayload{
		EndpointKey:       "brightness",
		SourceEndpointKey: "brightness",
		Value:             brightness,
		Brightness:        brightness,
		Power:             brightness > 0.5,
		DeviceID:          d.deviceID,
		SourceDeviceID:    d.deviceID,
	}

	// Send with ACK and wait for response
	return d.msgBus.SendWithAck(ctx, targetDriverID, "dimming.command", payload,
		driversdk.WithAckTimeout(5000), // 5 second timeout
		driversdk.WithTTL(10000),
		driversdk.WithPriority(driversdk.PriorityNormal),
	)
}

func (d *LightDimmerDriver) HandleCommand(ctx context.Context, cmd driversdk.Command) (driversdk.CommandResult, error) {
	if cmd.EndpointKey != "brightness" && cmd.EndpointKey != "power" {
		return driversdk.CommandResult{Success: false, Message: "unknown endpoint"}, fmt.Errorf("unknown endpoint: %s", cmd.EndpointKey)
	}

	if cmd.EndpointKey == "power" {
		var b bool
		if err := json.Unmarshal(cmd.Payload, &b); err != nil {
			return driversdk.CommandResult{Success: false, Message: "invalid payload"}, err
		}
		if !b {
			d.mu.Lock()
			cur := d.brightness
			if cur > 0.5 {
				d.lastNonZeroBrightness = cur
			}
			d.brightness = 0
			d.mu.Unlock()
			_ = d.publishBrightness(ctx, 0, driversdk.SourceUser)
			return driversdk.CommandResult{Success: true, Message: "ok"}, nil
		}
		d.mu.Lock()
		restore := d.lastNonZeroBrightness
		if restore <= 0.5 {
			restore = 100
		}
		d.brightness = restore
		d.mu.Unlock()
		_ = d.publishBrightness(ctx, restore, driversdk.SourceUser)
		return driversdk.CommandResult{Success: true, Message: "ok"}, nil
	}

	// Accept either a raw number (preferred) or {"brightness": n}.
	var rawNumber float64
	if err := json.Unmarshal(cmd.Payload, &rawNumber); err != nil {
		var obj struct {
			Brightness float64 `json:"brightness"`
		}
		if err2 := json.Unmarshal(cmd.Payload, &obj); err2 != nil {
			return driversdk.CommandResult{Success: false, Message: "invalid payload"}, err
		}
		rawNumber = obj.Brightness
	}

	v := clamp01To100(rawNumber)
	d.mu.Lock()
	d.brightness = v
	if v > 0.5 {
		d.lastNonZeroBrightness = v
	}
	d.mu.Unlock()

	_ = d.publishBrightness(ctx, v, driversdk.SourceUser)
	return driversdk.CommandResult{Success: true, Message: "ok"}, nil
}

func (d *LightDimmerDriver) Health(ctx context.Context) (driversdk.HealthStatus, map[string]string) {
	return driversdk.HealthOK, map[string]string{"since": time.Now().UTC().Format(time.RFC3339)}
}

func (d *LightDimmerDriver) Stop(ctx context.Context) error {
	d.stopOnce.Do(func() { close(d.stopCh) })

	// Stop the message bus
	if d.msgBus != nil {
		_ = d.msgBus.Stop(ctx)
	}

	d.wg.Wait()
	return nil
}
