package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	driversdk "github.com/NotrixInc/nx-driver-sdk"
)

type VortDCDimmerDriver struct {
	deviceID string

	deps   driversdk.Dependencies
	cfg    Config
	client *Client

	lastBrightness map[int]float64
	lastStrings    map[string]string
	lastNumbers    map[string]float64
	lastEnergyAt   time.Time
	upsertedMeta   bool

	stopOnce sync.Once
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

func NewVortDCDimmerDriver(deviceID string) *VortDCDimmerDriver {
	return &VortDCDimmerDriver{
		deviceID:       deviceID,
		stopCh:         make(chan struct{}),
		lastBrightness: map[int]float64{},
		lastStrings:    map[string]string{},
		lastNumbers:    map[string]float64{},
	}
}

func (d *VortDCDimmerDriver) ID() string                 { return "com.notrix.vort.dcdimmer" }
func (d *VortDCDimmerDriver) Version() string            { return "0.1.10" }
func (d *VortDCDimmerDriver) Type() driversdk.DriverType { return driversdk.DriverTypeDevice }
func (d *VortDCDimmerDriver) Protocols() []driversdk.Protocol {
	return []driversdk.Protocol{driversdk.ProtocolIP}
}
func (d *VortDCDimmerDriver) Topologies() []driversdk.Topology {
	return []driversdk.Topology{driversdk.TopologyDirectIP}
}

func (d *VortDCDimmerDriver) Init(ctx context.Context, deps driversdk.Dependencies, cfg driversdk.JsonConfig) error {
	d.deps = deps

	if err := cfg.Decode(&d.cfg); err != nil {
		return err
	}
	if d.cfg.IP == "" {
		return fmt.Errorf("config.ip is required")
	}
	if d.cfg.PollIntervalMs <= 0 {
		d.cfg.PollIntervalMs = 2000
	}
	if d.cfg.Channels <= 0 {
		d.cfg.Channels = 10
	}
	if d.cfg.Channels > 10 {
		d.cfg.Channels = 10
	}

	d.client = NewClient(d.cfg.IP, d.cfg.Port, d.cfg.Username, d.cfg.Password)
	return nil
}

func (d *VortDCDimmerDriver) Endpoints() ([]driversdk.Endpoint, error) {
	valueSchema, _ := json.Marshal(map[string]any{"type": "number", "minimum": 0, "maximum": 100})
	chs := d.cfg.Channels
	if chs <= 0 {
		chs = 10
	}
	if chs > 10 {
		chs = 10
	}
	eps := make([]driversdk.Endpoint, 0, chs)
	for ch := 1; ch <= chs; ch++ {
		eps = append(eps, driversdk.Endpoint{
			Key:          fmt.Sprintf("ch%d", ch),
			Name:         fmt.Sprintf("Channel %d", ch),
			Direction:    driversdk.EndpointDirectionInput,
			Kind:         driversdk.EndpointKindControl,
			Connection:   driversdk.EndpointConnection("DC_Dimmer"),
			Icon:         "ip",
			MultiBinding: true,
			ControlType:  "dimmer",
			ValueSchema:  valueSchema,
			Meta:         map[string]string{"channel": fmt.Sprintf("%d", ch)},
		})
	}
	return eps, nil
}

func (d *VortDCDimmerDriver) Variables() ([]driversdk.Variable, error) {
	vars := []driversdk.Variable{}
	// Writable dimming values per channel (0..100)
	chs := d.cfg.Channels
	if chs <= 0 {
		chs = 10
	}
	if chs > 10 {
		chs = 10
	}
	for ch := 1; ch <= chs; ch++ {
		vars = append(vars, driversdk.Variable{Key: fmt.Sprintf("ch%d", ch), Type: driversdk.VariableTypeNumber, Unit: "%", Readable: true, Writable: true})
	}

	vars = append(vars,
		driversdk.Variable{Key: "device_name", Type: driversdk.VariableTypeText, Unit: "", Readable: true, Writable: false},
		driversdk.Variable{Key: "model_name", Type: driversdk.VariableTypeText, Unit: "", Readable: true, Writable: false},
		driversdk.Variable{Key: "firmware_version", Type: driversdk.VariableTypeText, Unit: "", Readable: true, Writable: false},
		driversdk.Variable{Key: "input_voltage_1", Type: driversdk.VariableTypeNumber, Unit: "V", Readable: true, Writable: false},
		driversdk.Variable{Key: "input_voltage_2", Type: driversdk.VariableTypeNumber, Unit: "V", Readable: true, Writable: false},
		driversdk.Variable{Key: "current_ch1", Type: driversdk.VariableTypeNumber, Unit: "A", Readable: true, Writable: false},
		driversdk.Variable{Key: "current_ch2", Type: driversdk.VariableTypeNumber, Unit: "A", Readable: true, Writable: false},
		driversdk.Variable{Key: "current_ch3", Type: driversdk.VariableTypeNumber, Unit: "A", Readable: true, Writable: false},
		driversdk.Variable{Key: "current_ch4", Type: driversdk.VariableTypeNumber, Unit: "A", Readable: true, Writable: false},
		driversdk.Variable{Key: "current_ch5", Type: driversdk.VariableTypeNumber, Unit: "A", Readable: true, Writable: false},
		driversdk.Variable{Key: "current_ch6", Type: driversdk.VariableTypeNumber, Unit: "A", Readable: true, Writable: false},
		driversdk.Variable{Key: "current_ch7", Type: driversdk.VariableTypeNumber, Unit: "A", Readable: true, Writable: false},
		driversdk.Variable{Key: "current_ch8", Type: driversdk.VariableTypeNumber, Unit: "A", Readable: true, Writable: false},
		driversdk.Variable{Key: "current_ch9", Type: driversdk.VariableTypeNumber, Unit: "A", Readable: true, Writable: false},
		driversdk.Variable{Key: "current_ch10", Type: driversdk.VariableTypeNumber, Unit: "A", Readable: true, Writable: false},
		driversdk.Variable{Key: "energy_ch1_today", Type: driversdk.VariableTypeNumber, Unit: "Wh", Readable: true, Writable: false},
		driversdk.Variable{Key: "energy_ch2_today", Type: driversdk.VariableTypeNumber, Unit: "Wh", Readable: true, Writable: false},
		driversdk.Variable{Key: "energy_ch3_today", Type: driversdk.VariableTypeNumber, Unit: "Wh", Readable: true, Writable: false},
		driversdk.Variable{Key: "energy_ch4_today", Type: driversdk.VariableTypeNumber, Unit: "Wh", Readable: true, Writable: false},
		driversdk.Variable{Key: "energy_ch5_today", Type: driversdk.VariableTypeNumber, Unit: "Wh", Readable: true, Writable: false},
		driversdk.Variable{Key: "energy_ch6_today", Type: driversdk.VariableTypeNumber, Unit: "Wh", Readable: true, Writable: false},
		driversdk.Variable{Key: "energy_ch7_today", Type: driversdk.VariableTypeNumber, Unit: "Wh", Readable: true, Writable: false},
		driversdk.Variable{Key: "energy_ch8_today", Type: driversdk.VariableTypeNumber, Unit: "Wh", Readable: true, Writable: false},
		driversdk.Variable{Key: "energy_ch9_today", Type: driversdk.VariableTypeNumber, Unit: "Wh", Readable: true, Writable: false},
		driversdk.Variable{Key: "energy_ch10_today", Type: driversdk.VariableTypeNumber, Unit: "Wh", Readable: true, Writable: false},
		driversdk.Variable{Key: "energy_ch1_total", Type: driversdk.VariableTypeNumber, Unit: "Wh", Readable: true, Writable: false},
		driversdk.Variable{Key: "energy_ch2_total", Type: driversdk.VariableTypeNumber, Unit: "Wh", Readable: true, Writable: false},
		driversdk.Variable{Key: "energy_ch3_total", Type: driversdk.VariableTypeNumber, Unit: "Wh", Readable: true, Writable: false},
		driversdk.Variable{Key: "energy_ch4_total", Type: driversdk.VariableTypeNumber, Unit: "Wh", Readable: true, Writable: false},
		driversdk.Variable{Key: "energy_ch5_total", Type: driversdk.VariableTypeNumber, Unit: "Wh", Readable: true, Writable: false},
		driversdk.Variable{Key: "energy_ch6_total", Type: driversdk.VariableTypeNumber, Unit: "Wh", Readable: true, Writable: false},
		driversdk.Variable{Key: "energy_ch7_total", Type: driversdk.VariableTypeNumber, Unit: "Wh", Readable: true, Writable: false},
		driversdk.Variable{Key: "energy_ch8_total", Type: driversdk.VariableTypeNumber, Unit: "Wh", Readable: true, Writable: false},
		driversdk.Variable{Key: "energy_ch9_total", Type: driversdk.VariableTypeNumber, Unit: "Wh", Readable: true, Writable: false},
		driversdk.Variable{Key: "energy_ch10_total", Type: driversdk.VariableTypeNumber, Unit: "Wh", Readable: true, Writable: false},
	)
	return vars, nil
}

func (d *VortDCDimmerDriver) Start(ctx context.Context) error {
	// Upsert device + descriptors (driver-host typically handles this once per instance)
	desc := driversdk.DeviceDescriptor{
		DeviceID:           d.deviceID,
		DriverID:           d.ID(),
		ExternalDeviceKey:  "ip:" + d.cfg.IP,
		DisplayName:        "Notrix Vort DC Dimmer",
		DeviceType:         "DC_Dimmer",
		Manufacturer:       "Notrix inc.",
		Model:              "Vort",
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

	// Poll loop
	d.wg.Add(1)
	go d.pollLoop()

	// Control loop (apply desired controls quickly without waiting for full status poll).
	d.wg.Add(1)
	go d.controlLoop()

	return nil
}

func (d *VortDCDimmerDriver) controlLoop() {
	defer d.wg.Done()

	// Keep this reasonably fast so UI changes feel immediate.
	// PutProperty is only called when a desired value changes.
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()

	for {
		select {
		case <-d.stopCh:
			return
		case <-t.C:
			_ = d.applyDesiredControls()
		}
	}
}

func (d *VortDCDimmerDriver) pollLoop() {
	defer d.wg.Done()

	interval := time.Duration(d.cfg.PollIntervalMs) * time.Millisecond
	if interval <= 0 {
		interval = 2 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-d.stopCh:
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			st, err := d.client.GetBestStatus(ctx, d.cfg.Channels)
			cancel()
			if err != nil {
				d.deps.Logger.Warn("poll failed", "err", err.Error())
				// Publish a poll failure metric so the controller can track consecutive failures.
				_ = d.deps.Publisher.PublishVariable(context.Background(), driversdk.VariableUpdate{
					DeviceID: d.deviceID,
					Key:      "_poll_err",
					Value:    mustJSON(err.Error()),
					Quality:  driversdk.QualityBad,
					Source:   driversdk.SourceDriver,
					At:       d.deps.Clock.Now(),
				})
				continue
			}
			d.processStatus(st)
		}
	}
}

var chKeyRe = regexp.MustCompile(`^ch(\d+)$`)

func (d *VortDCDimmerDriver) HandleCommand(ctx context.Context, cmd driversdk.Command) (driversdk.CommandResult, error) {
	m := chKeyRe.FindStringSubmatch(cmd.EndpointKey)
	if m == nil {
		return driversdk.CommandResult{Success: false, Message: "unknown endpoint"}, fmt.Errorf("unknown endpoint: %s", cmd.EndpointKey)
	}
	var ch int
	_, _ = fmt.Sscanf(m[1], "%d", &ch)
	if ch < 1 || ch > 10 {
		return driversdk.CommandResult{Success: false, Message: "invalid channel"}, fmt.Errorf("invalid channel: %d", ch)
	}

	val, err := parseNumberPayload(cmd.Payload)
	if err != nil {
		return driversdk.CommandResult{Success: false, Message: "invalid payload"}, err
	}
	val = clamp(val, 0, 100)

	prop := fmt.Sprintf("channel%dDimmingValue", ch)
	if err := d.client.PutProperty(ctx, prop, val); err != nil {
		return driversdk.CommandResult{Success: false, Message: err.Error()}, err
	}

	// Publish immediate state update for the channel.
	state, _ := json.Marshal(map[string]any{fmt.Sprintf("ch%d", ch): val})
	_ = d.deps.Publisher.PublishState(ctx, driversdk.StateUpdate{DeviceID: d.deviceID, State: state, At: d.deps.Clock.Now()})
	// Also publish the channel value as a variable so UI control state updates immediately.
	_ = d.deps.Publisher.PublishVariable(ctx, driversdk.VariableUpdate{
		DeviceID: d.deviceID,
		Key:      fmt.Sprintf("ch%d", ch),
		Value:    mustJSON(val),
		Quality:  driversdk.QualityGood,
		Source:   driversdk.SourceDriver,
		At:       d.deps.Clock.Now(),
	})
	d.lastBrightness[ch] = val
	return driversdk.CommandResult{Success: true, Message: "ok"}, nil
}

func (d *VortDCDimmerDriver) Health(ctx context.Context) (driversdk.HealthStatus, map[string]string) {
	return driversdk.HealthOK, map[string]string{"ip": d.cfg.IP}
}

func (d *VortDCDimmerDriver) Stop(ctx context.Context) error {
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

func (d *VortDCDimmerDriver) processStatus(st Status) {
	now := d.deps.Clock.Now()

	// Heartbeat: publish a lightweight metric on every successful poll.
	// Without this, telemetry only updates when a value changes, which can make the UI
	// appear to flap Online/Offline when values are stable.
	_ = d.deps.Publisher.PublishVariable(context.Background(), driversdk.VariableUpdate{
		DeviceID: d.deviceID,
		Key:      "_poll_ok",
		Value:    mustJSON(1),
		Quality:  driversdk.QualityGood,
		Source:   driversdk.SourceDriver,
		At:       now,
	})

	// Upsert device metadata once we have real values.
	if !d.upsertedMeta {
		fw := statusString(st, "FirmwareVersion")
		name := statusString(st, "DeviceName")
		model := statusString(st, "ModelName")
		if fw != "" || name != "" || model != "" {
			desc := driversdk.DeviceDescriptor{
				DeviceID:           d.deviceID,
				DriverID:           d.ID(),
				ExternalDeviceKey:  "ip:" + d.cfg.IP,
				DisplayName:        firstNonEmpty(name, "Notrix Vort DC Dimmer"),
				DeviceType:         "DC_Dimmer",
				Manufacturer:       "Notrix inc.",
				Model:              firstNonEmpty(model, "Vort"),
				Firmware:           fw,
				IPAddress:          d.cfg.IP,
				ConnectionCategory: "DIRECT_IP",
				Protocol:           "IP",
				Meta:               map[string]string{},
			}
			_ = d.deps.Publisher.UpsertDevice(context.Background(), desc)
			d.upsertedMeta = true
		}
	}

	// State (brightness per channel)
	state := map[string]any{}
	changed := false
	chs := d.cfg.Channels
	if chs <= 0 {
		chs = 10
	}
	if chs > 10 {
		chs = 10
	}
	for ch := 1; ch <= chs; ch++ {
		prop := fmt.Sprintf("channel%dDimmingValue", ch)
		val, ok := statusFloat(st, prop)
		if !ok {
			continue
		}
		state[fmt.Sprintf("ch%d", ch)] = val
		last, has := d.lastBrightness[ch]
		if !has || math.Abs(last-val) > 0.5 {
			changed = true
			d.lastBrightness[ch] = val
		}
	}
	if changed {
		b, _ := json.Marshal(state)
		_ = d.deps.Publisher.PublishState(context.Background(), driversdk.StateUpdate{DeviceID: d.deviceID, State: b, At: now})
	}

	// Publish channel values as variables too, so the Control tab can display live state.
	for key, v := range state {
		f, ok := v.(float64)
		if !ok {
			continue
		}
		d.publishNumberIfChanged(key, f, now)
	}

	// Variables
	d.publishStringIfChanged("device_name", statusString(st, "DeviceName"), now)
	d.publishStringIfChanged("model_name", statusString(st, "ModelName"), now)
	d.publishStringIfChanged("firmware_version", statusString(st, "FirmwareVersion"), now)
	if v, ok := statusFloat(st, "inputVoltage1"); ok {
		d.publishNumberIfChanged("input_voltage_1", v, now)
	}
	if v, ok := statusFloat(st, "inputVoltage2"); ok {
		d.publishNumberIfChanged("input_voltage_2", v, now)
	}

	// Currents: publish on change
	for ch := 1; ch <= 10; ch++ {
		k := fmt.Sprintf("current_ch%d", ch)
		prop := fmt.Sprintf("currentCh%d", ch)
		if v, ok := statusFloat(st, prop); ok {
			d.publishNumberIfChanged(k, v, now)
		}
	}

	// Energy: rate-limit to once per minute
	if d.lastEnergyAt.IsZero() || now.Sub(d.lastEnergyAt) >= 60*time.Second {
		for ch := 1; ch <= 10; ch++ {
			kToday := fmt.Sprintf("energy_ch%d_today", ch)
			propToday := fmt.Sprintf("energyCh%dUsedToday", ch)
			if v, ok := statusFloat(st, propToday); ok {
				d.publishNumberIfChanged(kToday, v, now)
			}
			kTotal := fmt.Sprintf("energy_ch%d_total", ch)
			propTotal := fmt.Sprintf("energyCh%dUsedTotal", ch)
			if v, ok := statusFloat(st, propTotal); ok {
				d.publishNumberIfChanged(kTotal, v, now)
			}
		}
		d.lastEnergyAt = now
	}
}

func (d *VortDCDimmerDriver) publishStringIfChanged(key, val string, at time.Time) {
	if val == "" {
		return
	}
	if prev, ok := d.lastStrings[key]; ok && prev == val {
		return
	}
	d.lastStrings[key] = val
	b, _ := json.Marshal(val)
	_ = d.deps.Publisher.PublishVariable(context.Background(), driversdk.VariableUpdate{
		DeviceID: d.deviceID,
		Key:      key,
		Value:    b,
		Quality:  driversdk.QualityGood,
		Source:   driversdk.SourceDriver,
		At:       at,
	})
}

func (d *VortDCDimmerDriver) publishNumberIfChanged(key string, val float64, at time.Time) {
	if prev, ok := d.lastNumbers[key]; ok {
		if math.Abs(prev-val) < 0.0001 {
			return
		}
	}
	d.lastNumbers[key] = val
	b, _ := json.Marshal(val)
	_ = d.deps.Publisher.PublishVariable(context.Background(), driversdk.VariableUpdate{
		DeviceID: d.deviceID,
		Key:      key,
		Value:    b,
		Quality:  driversdk.QualityGood,
		Source:   driversdk.SourceDriver,
		At:       at,
	})
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

type coreDeviceResponse struct {
	MetaJSON map[string]any `json:"meta_json"`
}

func (d *VortDCDimmerDriver) applyDesiredControls() error {
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
	// Preferred: meta_json.controls (written by UI Control tab)
	desiredAny, ok := dev.MetaJSON["controls"].(map[string]any)
	if !ok || desiredAny == nil {
		// Backward-compat: if controls not present, fall back to variables.
		desiredAny, _ = dev.MetaJSON["variables"].(map[string]any)
	}
	if desiredAny == nil {
		return nil
	}

	chs := d.cfg.Channels
	if chs <= 0 {
		chs = 10
	}
	if chs > 10 {
		chs = 10
	}

	now := d.deps.Clock.Now()
	for ch := 1; ch <= chs; ch++ {
		key := fmt.Sprintf("ch%d", ch)
		desired, ok := coerceNumber(desiredAny[key])
		if !ok {
			continue
		}
		desired = clamp(desired, 0, 100)

		last, has := d.lastBrightness[ch]
		if has && math.Abs(last-desired) < 0.5 {
			continue
		}

		prop := fmt.Sprintf("channel%dDimmingValue", ch)
		ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
		err := d.client.PutProperty(ctx2, prop, desired)
		cancel2()
		if err != nil {
			d.deps.Logger.Warn("apply control failed", "key", key, "err", err.Error())
			continue
		}

		d.lastBrightness[ch] = desired
		d.publishNumberIfChanged(key, desired, now)
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
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func parseNumberPayload(b []byte) (float64, error) {
	var f float64
	if err := json.Unmarshal(b, &f); err == nil {
		return f, nil
	}
	var i int
	if err := json.Unmarshal(b, &i); err == nil {
		return float64(i), nil
	}
	var obj map[string]any
	if err := json.Unmarshal(b, &obj); err != nil {
		return 0, err
	}
	for _, k := range []string{"value", "brightness", "dimming"} {
		if v, ok := obj[k]; ok {
			if n, ok := v.(float64); ok {
				return n, nil
			}
		}
	}
	return 0, fmt.Errorf("expected number payload")
}

func clamp(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func firstNonEmpty(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}
