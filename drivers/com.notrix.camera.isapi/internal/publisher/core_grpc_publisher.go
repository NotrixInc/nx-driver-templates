package publisher

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	corev1 "github.com/NotrixInc/controller-platform/apps/controller-core/gen/go/core/v1"
	driversdk "github.com/NotrixInc/nx-driver-sdk"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type CoreGRPCPublisher struct {
	driverID          string
	externalDeviceKey string
	conn              *grpc.ClientConn
	seq               int64
	client            corev1.CoreServiceClient
	log               driversdk.Logger
}

func NewCoreGRPCPublisher(coreAddr, driverID, externalDeviceKey string, log driversdk.Logger) (*CoreGRPCPublisher, error) {
	coreAddr = strings.TrimSpace(coreAddr)
	driverID = strings.TrimSpace(driverID)
	externalDeviceKey = strings.TrimSpace(externalDeviceKey)
	if coreAddr == "" {
		return nil, fmt.Errorf("missing coreAddr")
	}
	if driverID == "" {
		return nil, fmt.Errorf("missing driverID")
	}
	if externalDeviceKey == "" {
		return nil, fmt.Errorf("missing externalDeviceKey")
	}
	if log == nil {
		log = driversdk.NewStdLogger()
	}

	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(
		dialCtx,
		coreAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("dial core grpc %q: %w", coreAddr, err)
	}

	return &CoreGRPCPublisher{
		driverID:          driverID,
		externalDeviceKey: externalDeviceKey,
		conn:              conn,
		client:            corev1.NewCoreServiceClient(conn),
		log:               log,
	}, nil
}

func (p *CoreGRPCPublisher) Close() error {
	if p == nil || p.conn == nil {
		return nil
	}
	return p.conn.Close()
}

// callContext bounds a publish so a wedged controller-core cannot stall the
// driver's own loops.
func (p *CoreGRPCPublisher) callContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, 2*time.Second)
}

// endpointKey addresses core by the identity the driver knows: its package id
// plus the stable external key for this device.
func (p *CoreGRPCPublisher) endpointKey(externalEndpointKey string) *corev1.EndpointKey {
	return &corev1.EndpointKey{
		DriverId:            p.driverID,
		ExternalDeviceKey:   p.externalDeviceKey,
		ExternalEndpointKey: strings.TrimSpace(externalEndpointKey),
	}
}

// tsMillis converts a driver timestamp, mapping the zero time to 0 rather than
// to the large negative value UnixMilli would produce. Core reads 0 as "now".
func tsMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func putIfSet(m map[string]string, key, value string) {
	if v := strings.TrimSpace(value); v != "" {
		m[key] = v
	}
}

func (p *CoreGRPCPublisher) UpsertDevice(ctx context.Context, d driversdk.DeviceDescriptor) error {
	if p == nil || p.client == nil {
		return nil
	}

	meta := map[string]string{}
	for k, v := range d.Meta {
		meta[k] = v
	}
	putIfSet(meta, "manufacturer", d.Manufacturer)
	putIfSet(meta, "model", d.Model)
	putIfSet(meta, "firmware", d.Firmware)
	putIfSet(meta, "ip", d.IPAddress)
	putIfSet(meta, "mac", d.MACAddress)
	putIfSet(meta, "connection_category", d.ConnectionCategory)
	putIfSet(meta, "protocol", d.Protocol)
	putIfSet(meta, "parent_device_id", d.ParentDeviceID)
	putIfSet(meta, "external_id", d.ExternalID)

	callCtx, cancel := p.callContext(ctx)
	defer cancel()

	_, err := p.client.UpsertDevice(callCtx, &corev1.UpsertDeviceRequest{
		Device: &corev1.DeviceDescriptor{
			DriverId:          p.driverID,
			ExternalDeviceKey: p.externalDeviceKey,
			DisplayName:       strings.TrimSpace(d.DisplayName),
			DeviceType:        strings.TrimSpace(d.DeviceType),
			Meta:              meta,
		},
	})
	if err != nil {
		p.log.Warn("upsert device failed", "err", err)
		return err
	}
	return nil
}

func (p *CoreGRPCPublisher) UpsertEndpoints(ctx context.Context, deviceID string, eps []driversdk.Endpoint) error {
	if p == nil || p.client == nil || len(eps) == 0 {
		return nil
	}

	var firstErr error
	for _, ep := range eps {
		key := strings.TrimSpace(ep.Key)
		if key == "" {
			continue
		}

		meta := map[string]string{}
		for k, v := range ep.Meta {
			meta[k] = v
		}
		// Core maps these onto device_endpoint columns; anything else stays in
		// the endpoint's meta blob.
		putIfSet(meta, "direction", string(ep.Direction))
		putIfSet(meta, "kind", string(ep.Kind))
		putIfSet(meta, "connection", string(ep.Connection))
		putIfSet(meta, "icon", ep.Icon)
		putIfSet(meta, "control_type", ep.ControlType)

		endpointType := ep.ControlType
		if strings.TrimSpace(endpointType) == "" {
			endpointType = ep.Type
		}
		if strings.TrimSpace(endpointType) == "" {
			endpointType = string(ep.Kind)
		}
		putIfSet(meta, "endpoint_type", endpointType)

		if len(ep.ValueSchema) > 0 {
			meta["value_schema"] = string(ep.ValueSchema)
		}
		if ep.MultiBinding {
			meta["multi_binding"] = "true"
		}

		callCtx, cancel := p.callContext(ctx)
		_, err := p.client.UpsertEndpoint(callCtx, &corev1.UpsertEndpointRequest{
			DriverId:          p.driverID,
			ExternalDeviceKey: p.externalDeviceKey,
			Endpoint: &corev1.EndpointDescriptor{
				ExternalEndpointKey: key,
				DisplayName:         strings.TrimSpace(ep.Name),
				Meta:                meta,
			},
		})
		cancel()

		if err != nil {
			p.log.Warn("upsert endpoint failed", "endpoint", key, "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// UpsertVariables has no counterpart in core.v1: variable schemas ship with the
// driver package (variables.schema.json) and values arrive through
// PublishVariable. Kept to satisfy the SDK's Publisher interface.
func (p *CoreGRPCPublisher) UpsertVariables(ctx context.Context, deviceID string, vars []driversdk.Variable) error {
	return nil
}

func (p *CoreGRPCPublisher) PublishState(ctx context.Context, s driversdk.StateUpdate) error {
	if p == nil || p.client == nil {
		return nil
	}

	state := strings.TrimSpace(string(s.State))
	if state == "" {
		return nil
	}

	callCtx, cancel := p.callContext(ctx)
	defer cancel()

	_, err := p.client.PublishState(callCtx, &corev1.PublishStateRequest{
		Endpoint:  p.endpointKey(""),
		StateJson: state,
		TsUnixMs:  tsMillis(s.At),
		Seq:       atomic.AddInt64(&p.seq, 1),
	})
	if err != nil {
		p.log.Warn("publish state failed", "err", err)
		return err
	}
	return nil
}

func (p *CoreGRPCPublisher) PublishEvent(ctx context.Context, e driversdk.DeviceEvent) error {
	if p == nil || p.client == nil {
		return nil
	}

	eventType := strings.TrimSpace(e.Type)
	if eventType == "" {
		return nil
	}

	// core.v1 has no severity field, so it rides inside the payload, which is
	// where controller-core reads it from.
	payload := map[string]any{}
	if trimmed := strings.TrimSpace(string(e.Payload)); trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
			payload = map[string]any{"raw": trimmed}
		}
	}
	if e.Severity != "" {
		payload["severity"] = string(e.Severity)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		payloadJSON = []byte("{}")
	}

	callCtx, cancel := p.callContext(ctx)
	defer cancel()

	_, err = p.client.PublishEvent(callCtx, &corev1.PublishEventRequest{
		Endpoint:    p.endpointKey(""),
		EventTypeId: eventType,
		PayloadJson: string(payloadJSON),
		TsUnixMs:    tsMillis(e.At),
	})
	if err != nil {
		p.log.Warn("publish event failed", "type", eventType, "err", err)
		return err
	}
	return nil
}

func (p *CoreGRPCPublisher) PublishVariable(ctx context.Context, v driversdk.VariableUpdate) error {
	if p == nil || p.client == nil {
		return nil
	}
	metric := strings.TrimSpace(v.Key)
	if metric == "" {
		return nil
	}

	tags := map[string]string{}
	if v.Quality != "" {
		tags["quality"] = string(v.Quality)
	}
	if v.Source != "" {
		tags["source"] = string(v.Source)
	}

	req := &corev1.PublishTelemetryRequest{
		Endpoint: &corev1.EndpointKey{DriverId: p.driverID, ExternalDeviceKey: p.externalDeviceKey},
		Metric:   metric,
		Tags:     tags,
		TsUnixMs: tsMillis(v.At),
	}

	trimmed := strings.TrimSpace(string(v.Value))
	if trimmed != "" {
		var f float64
		if err := json.Unmarshal([]byte(trimmed), &f); err == nil {
			req.ValueNum = f
		} else {
			req.ValueJson = trimmed
		}
	}

	callCtx := ctx
	if callCtx == nil {
		callCtx = context.Background()
	}
	callCtx, cancel := context.WithTimeout(callCtx, 2*time.Second)
	defer cancel()

	_, err := p.client.PublishTelemetry(callCtx, req)
	if err != nil {
		p.log.Warn("publish telemetry failed", "metric", metric, "err", err)
		return err
	}
	return nil
}
