package publisher

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(
		ctx,
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

func (p *CoreGRPCPublisher) UpsertDevice(ctx context.Context, d driversdk.DeviceDescriptor) error {
	return nil
}

func (p *CoreGRPCPublisher) UpsertEndpoints(ctx context.Context, deviceID string, eps []driversdk.Endpoint) error {
	return nil
}

func (p *CoreGRPCPublisher) UpsertVariables(ctx context.Context, deviceID string, vars []driversdk.Variable) error {
	return nil
}

func (p *CoreGRPCPublisher) PublishState(ctx context.Context, s driversdk.StateUpdate) error {
	return nil
}

func (p *CoreGRPCPublisher) PublishEvent(ctx context.Context, e driversdk.DeviceEvent) error {
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
		Endpoint: &corev1.EndpointKey{
			DriverId:          p.driverID,
			ExternalDeviceKey: p.externalDeviceKey,
		},
		Metric:   metric,
		Tags:     tags,
		TsUnixMs: v.At.UnixMilli(),
	}

	// If Value is a simple JSON number, send it as value_num; otherwise send raw JSON.
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
