package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	driversdk "github.com/NotrixInc/nx-driver-sdk"
)

const (
	driverID      = "com.notrix.w2io"
	driverVersion = "0.1.0"
)

type W2IODriver struct {
	deviceID string

	deps   driversdk.Dependencies
	cfg    Config
	client *Client

	pollTicker *time.Ticker
	stopCh     chan struct{}
	wg         sync.WaitGroup

	lastMu sync.Mutex
	last   map[string]json.RawMessage
}

func NewW2IODriver(deviceID string) *W2IODriver {
	return &W2IODriver{deviceID: deviceID, stopCh: make(chan struct{}), last: map[string]json.RawMessage{}}
}

func (d *W2IODriver) ID() string                 { return driverID }
func (d *W2IODriver) Version() string            { return driverVersion }
func (d *W2IODriver) Type() driversdk.DriverType { return driversdk.DriverTypeDevice }
func (d *W2IODriver) Protocols() []driversdk.Protocol {
	return []driversdk.Protocol{driversdk.ProtocolIP}
}
func (d *W2IODriver) Topologies() []driversdk.Topology {
	return []driversdk.Topology{driversdk.TopologyDirectIP}
}

func (d *W2IODriver) Init(ctx context.Context, deps driversdk.Dependencies, cfg driversdk.JSONConfig) error {
	d.deps = deps
	if err := cfg.Decode(&d.cfg); err != nil {
		return err
	}
	if d.cfg.PollIntervalMs <= 0 {
		d.cfg.PollIntervalMs = 2000
	}
	if d.cfg.PollIntervalMs < 250 {
		d.cfg.PollIntervalMs = 250
	}
	if d.cfg.PollIntervalMs > 60000 {
		d.cfg.PollIntervalMs = 60000
	}

	client, err := NewClient(d.cfg)
	if err != nil {
		return err
	}
	d.client = client
	return nil
}

func (d *W2IODriver) Endpoints() ([]driversdk.Endpoint, error) {
	emptyObj, _ := json.Marshal(map[string]any{"type": "object", "properties": map[string]any{}})
	setSchema, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"key":   map[string]any{"type": "string"},
			"value": map[string]any{},
		},
		"required": []string{"key", "value"},
	})

	return []driversdk.Endpoint{
		{
			Key:          "refresh_status",
			Name:         "Refresh Status",
			Direction:    driversdk.EndpointDirectionOutput,
			Kind:         driversdk.EndpointKindControl,
			Connection:   driversdk.EndpointConnectionIP,
			Icon:         "refresh",
			MultiBinding: false,
			ControlType:  "button",
			ValueSchema:  emptyObj,
			Meta:         map[string]string{},
		},
		{
			Key:          "set_value",
			Name:         "Set Device Value",
			Direction:    driversdk.EndpointDirectionOutput,
			Kind:         driversdk.EndpointKindControl,
			Connection:   driversdk.EndpointConnectionIP,
			Icon:         "ip",
			MultiBinding: false,
			ControlType:  "form",
			ValueSchema:  setSchema,
			Meta:         map[string]string{},
		},
	}, nil
}

func mapVariableType(t string) driversdk.VariableType {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "boolean":
		return driversdk.VariableTypeBoolean
	case "number", "range":
		return driversdk.VariableTypeNumber
	case "password":
		return driversdk.VariableTypePassword
	case "image":
		return driversdk.VariableTypeImage
	case "video":
		return driversdk.VariableTypeVideo
	default:
		return driversdk.VariableTypeText
	}
}

func (d *W2IODriver) Variables() ([]driversdk.Variable, error) {
	out := make([]driversdk.Variable, 0, len(variableDefinitions))
	for _, v := range variableDefinitions {
		out = append(out, driversdk.Variable{
			Key:      v.Key,
			Type:     mapVariableType(v.Type),
			Unit:     v.Unit,
			Readable: v.Readable,
			Writable: v.Writable,
		})
	}
	return out, nil
}

func (d *W2IODriver) Start(ctx context.Context) error {
	desc := driversdk.DeviceDescriptor{
		DeviceID:           d.deviceID,
		DriverID:           d.ID(),
		ExternalDeviceKey:  "w2io:" + d.cfg.BaseURL,
		DisplayName:        "W2IO",
		DeviceType:         "sensor",
		ConnectionCategory: "DIRECT_IP",
		Protocol:           "IP",
		IPAddress:          d.cfg.BaseURL,
		Meta:               map[string]string{},
	}
	_ = d.deps.Publisher.UpsertDevice(ctx, desc)

	eps, _ := d.Endpoints()
	_ = d.deps.Publisher.UpsertEndpoints(ctx, d.deviceID, eps)

	vars, _ := d.Variables()
	_ = d.deps.Publisher.UpsertVariables(ctx, d.deviceID, vars)

	if err := d.refreshOnce(ctx); err != nil && d.deps.Logger != nil {
		d.deps.Logger.Warn("initial refresh failed", "err", err)
	}

	d.pollTicker = time.NewTicker(time.Duration(d.cfg.PollIntervalMs) * time.Millisecond)
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-d.stopCh:
				return
			case <-d.pollTicker.C:
				if err := d.refreshOnce(ctx); err != nil && d.deps.Logger != nil {
					d.deps.Logger.Warn("poll refresh failed", "err", err)
				}
			}
		}
	}()

	return nil
}

func (d *W2IODriver) parseNormalizedValue(raw any) (json.RawMessage, error) {
	switch v := raw.(type) {
	case bool:
		return json.Marshal(v)
	case float64:
		return json.Marshal(v)
	case int:
		return json.Marshal(v)
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return json.Marshal("")
		}
		ls := strings.ToLower(s)
		if ls == "true" || ls == "false" {
			return json.Marshal(ls == "true")
		}
		if f, ok := parseNumberLikeString(s); ok {
			return json.Marshal(f)
		}
		return json.Marshal(s)
	default:
		return json.Marshal(v)
	}
}

func parseNumberLikeString(s string) (float64, bool) {
	if idx := strings.Index(s, "JS:"); idx >= 0 {
		candidate := strings.TrimSpace(s[idx+3:])
		if f, err := strconv.ParseFloat(candidate, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			return f, true
		}
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
		return f, true
	}
	return 0, false
}

func (d *W2IODriver) refreshOnce(ctx context.Context) error {
	data, err := d.client.GetStatus(ctx)
	if err != nil {
		return err
	}
	for _, def := range variableDefinitions {
		raw, ok := data[def.Key]
		if !ok {
			continue
		}
		normalized, err := d.parseNormalizedValue(raw)
		if err != nil {
			continue
		}
		if d.isUnchanged(def.Key, normalized) {
			continue
		}
		_ = d.deps.Publisher.PublishVariable(ctx, driversdk.VariableUpdate{
			DeviceID: d.deviceID,
			Key:      def.Key,
			Value:    normalized,
			Quality:  driversdk.QualityGood,
			Source:   driversdk.SourceDriver,
			At:       d.deps.Clock.Now(),
		})
	}
	return nil
}

func (d *W2IODriver) isUnchanged(key string, value json.RawMessage) bool {
	d.lastMu.Lock()
	defer d.lastMu.Unlock()
	if prev, ok := d.last[key]; ok && string(prev) == string(value) {
		return true
	}
	d.last[key] = append(json.RawMessage{}, value...)
	return false
}

func (d *W2IODriver) HandleCommand(ctx context.Context, cmd driversdk.Command) (driversdk.CommandResult, error) {
	switch cmd.EndpointKey {
	case "refresh_status":
		if err := d.refreshOnce(ctx); err != nil {
			return driversdk.CommandResult{Success: false, Message: err.Error()}, err
		}
		return driversdk.CommandResult{Success: true, Message: "ok"}, nil
	case "set_value":
		var payload struct {
			Key   string `json:"key"`
			Value any    `json:"value"`
		}
		if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
			return driversdk.CommandResult{Success: false, Message: "invalid payload"}, err
		}
		payload.Key = strings.TrimSpace(payload.Key)
		if payload.Key == "" {
			return driversdk.CommandResult{Success: false, Message: "key is required"}, fmt.Errorf("missing key")
		}
		if err := d.client.SetValue(ctx, payload.Key, payload.Value); err != nil {
			return driversdk.CommandResult{Success: false, Message: err.Error()}, err
		}
		_ = d.refreshOnce(ctx)
		return driversdk.CommandResult{Success: true, Message: "ok"}, nil
	default:
		return driversdk.CommandResult{Success: false, Message: "unknown endpoint"}, fmt.Errorf("unknown endpoint: %s", cmd.EndpointKey)
	}
}

func (d *W2IODriver) Health(ctx context.Context) (driversdk.HealthStatus, map[string]string) {
	if err := d.refreshOnce(ctx); err != nil {
		return driversdk.HealthDegraded, map[string]string{"error": err.Error()}
	}
	return driversdk.HealthOK, map[string]string{"base_url": d.cfg.BaseURL}
}

func (d *W2IODriver) Stop(ctx context.Context) error {
	select {
	case <-d.stopCh:
	default:
		close(d.stopCh)
	}
	if d.pollTicker != nil {
		d.pollTicker.Stop()
	}
	d.wg.Wait()
	return nil
}
