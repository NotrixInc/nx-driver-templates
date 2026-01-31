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

	mu sync.Mutex

	lastBrightness map[int]float64
	lastBusAt      map[int]time.Time
	lastBusValue   map[int]float64
	lastStrings    map[string]string
	lastNumbers    map[string]float64
	lastEnergyAt   time.Time
	lastControlsTS int64
	upsertedMeta   bool

	stopOnce sync.Once
	stopCh   chan struct{}
	wg       sync.WaitGroup

	msgClient *driversdk.DriverMessageClient
	msgAfter  int64

	bindingsMu            sync.Mutex
	bindingsAt            time.Time
	bindingsByTargetEpKey map[string][]bindingSource
}

type bindingSource struct {
	SourceDeviceID   string
	SourceEndpointID string
}

type coreBinding struct {
	BindingType      string  `json:"binding_type"`
	Enabled          *bool   `json:"enabled"`
	SourceDeviceID   *string `json:"source_device_id"`
	SourceEndpointID *string `json:"source_endpoint_id"`
	TargetDeviceID   *string `json:"target_device_id"`
	TargetEndpointID *string `json:"target_endpoint_id"`
}

func NewVortDCDimmerDriver(deviceID string) *VortDCDimmerDriver {
	return &VortDCDimmerDriver{
		deviceID:       deviceID,
		stopCh:         make(chan struct{}),
		lastBrightness: map[int]float64{},
		lastBusAt:      map[int]time.Time{},
		lastBusValue:   map[int]float64{},
		lastStrings:    map[string]string{},
		lastNumbers:    map[string]float64{},
	}
}

func (d *VortDCDimmerDriver) ID() string                 { return "com.notrix.vort.dcdimmer" }
func (d *VortDCDimmerDriver) Version() string            { return "0.1.18" }
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

	// Listen for driver-to-driver messages (strict allow-list is enforced by controller-core).
	d.msgClient = driversdk.NewDriverMessageClientFromEnv()
	d.fastForwardDriverMessages()
	d.wg.Add(1)
	go d.messageLoop()

	return nil
}

func (d *VortDCDimmerDriver) listBindings(ctx context.Context) ([]coreBinding, error) {
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

func (d *VortDCDimmerDriver) refreshBindingCache() {
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	rows, err := d.listBindings(ctx)
	cancel()
	if err != nil {
		return
	}

	byTarget := map[string][]bindingSource{}
	for _, b := range rows {
		typeRaw := strings.TrimSpace(b.BindingType)
		if typeRaw != "ENDPOINT_TO_ENDPOINT" && typeRaw != "DEVICE_ENDPOINT" {
			continue
		}
		if b.Enabled != nil && !*b.Enabled {
			continue
		}

		tgtDev := strings.TrimSpace(derefString(b.TargetDeviceID))
		if tgtDev == "" || !strings.EqualFold(tgtDev, strings.TrimSpace(d.deviceID)) {
			continue
		}
		tgtEp := strings.TrimSpace(derefString(b.TargetEndpointID))
		if tgtEp == "" {
			continue
		}
		srcDev := strings.TrimSpace(derefString(b.SourceDeviceID))
		if srcDev == "" {
			continue
		}
		srcEp := strings.TrimSpace(derefString(b.SourceEndpointID))
		byTarget[tgtEp] = append(byTarget[tgtEp], bindingSource{SourceDeviceID: srcDev, SourceEndpointID: srcEp})
	}

	d.bindingsMu.Lock()
	d.bindingsByTargetEpKey = byTarget
	d.bindingsAt = time.Now()
	d.bindingsMu.Unlock()
}

func (d *VortDCDimmerDriver) getBindingSources(targetEndpointKey string) []bindingSource {
	targetEndpointKey = strings.TrimSpace(targetEndpointKey)
	if targetEndpointKey == "" {
		return nil
	}

	d.bindingsMu.Lock()
	stale := d.bindingsByTargetEpKey == nil || time.Since(d.bindingsAt) > 3*time.Second
	d.bindingsMu.Unlock()
	if stale {
		d.refreshBindingCache()
	}

	d.bindingsMu.Lock()
	defer d.bindingsMu.Unlock()
	items := d.bindingsByTargetEpKey[targetEndpointKey]
	out := make([]bindingSource, 0, len(items))
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

func (d *VortDCDimmerDriver) fastForwardDriverMessages() {
	if d.msgClient == nil {
		return
	}

	after := d.msgAfter
	for i := 0; i < 6; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		resp, err := d.msgClient.Poll(ctx, d.ID(), after, 0, 500)
		cancel()
		if err != nil {
			return
		}
		if resp == nil || len(resp.Messages) == 0 {
			break
		}
		for _, m := range resp.Messages {
			if m.ID > after {
				after = m.ID
			}
		}
	}
	d.msgAfter = after
}

func (d *VortDCDimmerDriver) messageLoop() {
	defer d.wg.Done()

	for {
		select {
		case <-d.stopCh:
			return
		default:
		}

		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		resp, err := d.msgClient.Poll(ctx, d.ID(), d.msgAfter, 10*time.Second, 200)
		cancel()
		if err != nil {
			if d.deps.Logger != nil {
				d.deps.Logger.Debug("driver-messages poll failed", "err", err.Error())
			}
			time.Sleep(750 * time.Millisecond)
			continue
		}

		for _, m := range resp.Messages {
			if m.ID > d.msgAfter {
				d.msgAfter = m.ID
			}
			d.handleDriverMessage(m)
		}
	}
}

func (d *VortDCDimmerDriver) handleDriverMessage(m driversdk.DriverMessage) {
	if m.Type != "dimming.changed" && m.Type != "dimming.set" {
		return
	}
	// Drop stale queued messages to avoid replays after restarts.
	if m.TSUnixMs > 0 {
		age := time.Now().UnixMilli() - m.TSUnixMs
		if age > 30_000 {
			return
		}
	}
	// Extra safety: ignore our own broadcasts.
	if strings.EqualFold(strings.TrimSpace(m.SourceDriver), d.ID()) {
		return
	}

	var payload map[string]any
	if err := json.Unmarshal(m.Payload, &payload); err != nil {
		return
	}

	if tid, ok := payload["target_device_id"].(string); ok {
		if strings.TrimSpace(tid) != "" && !strings.EqualFold(strings.TrimSpace(tid), strings.TrimSpace(d.deviceID)) {
			return
		}
	}

	valAny, ok := payload["value"]
	if !ok {
		valAny, ok = payload["brightness"]
		if !ok {
			return
		}
	}
	val, ok := coerceNumber(valAny)
	if !ok {
		return
	}
	val = clamp(val, 0, 100)

	// Determine target channel(s).
	ch := 0
	if v, ok := payload["channel"]; ok {
		if n, ok := coerceNumber(v); ok {
			ch = int(n)
		}
	}
	if ch <= 0 {
		if v, ok := payload["endpoint_key"].(string); ok {
			v = strings.TrimSpace(v)
			if m := chKeyRe.FindStringSubmatch(v); m != nil {
				_, _ = fmt.Sscanf(m[1], "%d", &ch)
			}
			if strings.EqualFold(v, "brightness") {
				ch = 1
			}
		}
	}

	if ch > 0 {
		_ = d.applyChannelBrightness(ch, val, driversdk.SourceDriver, "")
		return
	}

	// If no channel was specified, apply to all configured channels.
	chs := d.cfg.Channels
	if chs <= 0 {
		chs = 10
	}
	if chs > 10 {
		chs = 10
	}
	for i := 1; i <= chs; i++ {
		_ = d.applyChannelBrightness(i, val, driversdk.SourceDriver, "")
	}
}

func (d *VortDCDimmerDriver) applyChannelBrightness(ch int, val float64, source driversdk.Source, correlationID string) error {
	if ch < 1 || ch > 10 {
		return nil
	}
	val = clamp(val, 0, 100)

	prop := fmt.Sprintf("channel%dDimmingValue", ch)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	err := d.client.PutProperty(ctx, prop, val)
	cancel()
	if err != nil {
		return err
	}

	now := d.deps.Clock.Now()
	key := fmt.Sprintf("ch%d", ch)

	// Publish immediate state update for the channel.
	state, _ := json.Marshal(map[string]any{key: val})
	_ = d.deps.Publisher.PublishState(context.Background(), driversdk.StateUpdate{DeviceID: d.deviceID, State: state, At: now})
	_ = d.deps.Publisher.PublishVariable(context.Background(), driversdk.VariableUpdate{
		DeviceID: d.deviceID,
		Key:      key,
		Value:    mustJSON(val),
		Quality:  driversdk.QualityGood,
		Source:   source,
		At:       now,
	})
	d.mu.Lock()
	d.lastBrightness[ch] = val
	d.mu.Unlock()

	return nil
}

func (d *VortDCDimmerDriver) maybeBroadcastChannelBrightness(ch int, val float64, at time.Time) {
	if d.msgClient == nil {
		return
	}

	key := fmt.Sprintf("ch%d", ch)
	boundSources := d.getBindingSources(key)
	if len(boundSources) == 0 {
		return
	}

	// Keep this conservative to avoid spamming the bus.
	// Guard maps: pollLoop/controlLoop run concurrently.
	d.mu.Lock()
	if lastAt, ok := d.lastBusAt[ch]; ok {
		if at.Sub(lastAt) < 750*time.Millisecond {
			d.mu.Unlock()
			return
		}
	}
	if lastVal, ok := d.lastBusValue[ch]; ok {
		if math.Abs(lastVal-val) <= 0.5 {
			d.mu.Unlock()
			return
		}
	}
	d.lastBusAt[ch] = at
	d.lastBusValue[ch] = val
	d.mu.Unlock()

	for _, s := range boundSources {
		payload, _ := json.Marshal(map[string]any{
			"endpoint_key":        strings.TrimSpace(s.SourceEndpointID),
			"source_endpoint_key": key,
			"channel":             ch,
			"value":               val,
			"device_id":           d.deviceID,
			"source_device_id":    d.deviceID,
			"target_device_id":    strings.TrimSpace(s.SourceDeviceID),
		})
		ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
		_, err := d.msgClient.Publish(ctx, d.ID(), "", "dimming.changed", json.RawMessage(payload), "")
		cancel()
		if err != nil && d.deps.Logger != nil {
			d.deps.Logger.Debug("driver-messages publish failed", "err", err.Error())
		}
	}
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
					Quality:  driversdk.QualityUnknown,
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

	if err := d.applyChannelBrightness(ch, val, driversdk.SourceUser, cmd.CorrelationID); err != nil {
		return driversdk.CommandResult{Success: false, Message: err.Error()}, err
	}
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
		d.mu.Lock()
		last, has := d.lastBrightness[ch]
		d.mu.Unlock()
		if !has || math.Abs(last-val) > 0.5 {
			changed = true
			d.mu.Lock()
			d.lastBrightness[ch] = val
			d.mu.Unlock()
			d.maybeBroadcastChannelBrightness(ch, val, now)
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

	// Only apply explicit desired controls. Falling back to variables causes feedback loops
	// (poll telemetry -> variables -> treated as desired -> echoed to bus).
	desiredAny, ok := dev.MetaJSON["controls"].(map[string]any)
	if !ok || desiredAny == nil {
		return nil
	}

	// Avoid hammering the device (and re-sending the same value) when the UI/driver
	// hasn't actually issued a new desired control update.
	// NOTE: controller-core and the web-console set controls_ts_unix_ms on writes.
	if tsAny, ok := dev.MetaJSON["controls_ts_unix_ms"]; ok {
		if tsF, ok := coerceNumber(tsAny); ok {
			ts := int64(tsF)
			if ts > 0 {
				if ts <= d.lastControlsTS {
					return nil
				}
				d.lastControlsTS = ts
			}
		}
	}

	// Desired controls coming from controller-core should not be broadcast to other drivers.
	// (This prevents message-bus echo loops; the originating driver/UI should broadcast.)
	src := driversdk.SourceDriver

	chs := d.cfg.Channels
	if chs <= 0 {
		chs = 10
	}
	if chs > 10 {
		chs = 10
	}

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

		// Apply desired controls. Never broadcast controller-core desired controls to the bus.
		if err := d.applyChannelBrightness(ch, desired, src, ""); err != nil {
			d.deps.Logger.Warn("apply control failed", "key", key, "err", err.Error())
			continue
		}
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
