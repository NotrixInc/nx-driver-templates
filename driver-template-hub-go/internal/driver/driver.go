package driver

import (
  "context"
  "encoding/json"
  "fmt"
  "sync"
  "time"

  "github.com/NotrixInc/nx-driver-sdk"
  "github.com/NotrixInc/nx-driver-sdk/hostrpc"
  "google.golang.org/grpc"
  "google.golang.org/grpc/credentials/insecure"
  "os"

)

type ExampleHubDriver struct {
  hostGRPCAddr string
  grpcConn *grpc.ClientConn
  hubStream hostrpc.HubGatewayService_OpenHubSessionClient

  deviceID string
  deps driversdk.Dependencies
  cfg Config

  stopOnce sync.Once
  stopCh chan struct{}
  wg sync.WaitGroup

  permitJoin bool
  // mock inventory
  children []driversdk.ChildCandidate
}

func NewExampleHubDriver(deviceID string) *ExampleHubDriver {
  return &ExampleHubDriver{
    deviceID: deviceID,
    stopCh: make(chan struct{}),
    children: []driversdk.ChildCandidate{
      {
        Protocol: driversdk.ProtocolZigbee,
        ChildRef: "0x00158d0001a2b3c4",
        Manufacturer: "Example",
        Model: "ZB-Dimmer",
        Firmware: "1.0",
        Fingerprint: json.RawMessage(`{"clusters":["0x0006","0x0008"]}`),
      },
    },
  }
}

func (d *ExampleHubDriver) ID() string { return "com.example.template.hub" }
func (d *ExampleHubDriver) Version() string { return "1.0.0" }
func (d *ExampleHubDriver) Type() driversdk.DriverType { return driversdk.DriverTypeHub }
func (d *ExampleHubDriver) Protocols() []driversdk.Protocol { return []driversdk.Protocol{driversdk.ProtocolIP} }
func (d *ExampleHubDriver) Topologies() []driversdk.Topology { return []driversdk.Topology{driversdk.TopologyDirectIP} }

func (d *ExampleHubDriver) Init(ctx context.Context, deps driversdk.Dependencies, cfg driversdk.JSONConfig) error {
  d.hostGRPCAddr = os.Getenv("NX_HOST_GRPC_ADDR")

  d.deps = deps
  if err := cfg.Decode(&d.cfg); err != nil { return err }
  if d.cfg.IP == "" { return fmt.Errorf("config.ip required") }
  if d.cfg.PollIntervalSeconds <= 0 { d.cfg.PollIntervalSeconds = 5 }
  return nil
}

func (d *ExampleHubDriver) Endpoints() ([]driversdk.Endpoint, error) {
  sch, _ := json.Marshal(map[string]any{"type":"boolean"})
  return []driversdk.Endpoint{
    { Key:"permit_join", Name:"Permit Join", Direction:"BIDIR", Type:"toggle", ValueSchema: sch },
  }, nil
}

func (d *ExampleHubDriver) Variables() ([]driversdk.Variable, error) {
  return []driversdk.Variable{
    { Key:"child_count", Type:"integer", Unit:"", ReadOnly:true },
  }, nil
}

func (d *ExampleHubDriver) Start(ctx context.Context) error {
  // Upsert hub device + descriptors (driver-host typically orchestrates this)
  desc := driversdk.DeviceDescriptor{
    DeviceID: d.deviceID,
    DriverID: d.ID(),
    ExternalDeviceKey: "ip:"+d.cfg.IP,
    DisplayName: "Example Hub",
    DeviceType: "hub",
    Manufacturer: "Example",
    Model: "TemplateHub",
    IPAddress: d.cfg.IP,
    ConnectionCategory: "DIRECT_IP",
    Protocol: "IP",
  }
  _ = d.deps.Publisher.UpsertDevice(ctx, desc)

  eps,_ := d.Endpoints()
  _ = d.deps.Publisher.UpsertEndpoints(ctx, d.deviceID, eps)

  vars,_ := d.Variables()
  _ = d.deps.Publisher.UpsertVariables(ctx, d.deviceID, vars)

  d.wg.Add(1)
  go d.pollLoop()
  // Start internal gRPC hub gateway session (CHILD->HOST->HUB)
  go d.startHubGatewaySession()
  return nil
}

func (d *ExampleHubDriver) pollLoop() {
  defer d.wg.Done()
  t := time.NewTicker(time.Duration(d.cfg.PollIntervalSeconds)*time.Second)
  defer t.Stop()

  for {
    select {
    case <-d.stopCh:
      return
    case <-t.C:
      // publish hub state + child_count telemetry
      st,_ := json.Marshal(map[string]any{
        "permit_join": d.permitJoin,
      })
      _ = d.deps.Publisher.PublishState(context.Background(), driversdk.StateUpdate{
        DeviceID: d.deviceID, State: st, At: d.deps.Clock.Now(),
      })
      v,_ := json.Marshal(len(d.children))
      _ = d.deps.Publisher.PublishVariable(context.Background(), driversdk.VariableUpdate{
        DeviceID: d.deviceID, Key:"child_count", Value: v, Quality: driversdk.QualityGood, Source: driversdk.SourceDriver, At: d.deps.Clock.Now(),
      })
    }
  }
}

func (d *ExampleHubDriver) HandleCommand(ctx context.Context, cmd driversdk.Command) (driversdk.CommandResult, error) {
  switch cmd.EndpointKey {
  case "permit_join":
    var on bool
    if err := json.Unmarshal(cmd.Payload, &on); err != nil {
      return driversdk.CommandResult{Success:false, Message:"invalid payload"}, err
    }
    d.permitJoin = on
    return driversdk.CommandResult{Success:true, Message:"ok"}, nil
  default:
    return driversdk.CommandResult{Success:false, Message:"unknown endpoint"}, fmt.Errorf("unknown endpoint: %s", cmd.EndpointKey)
  }
}

func (d *ExampleHubDriver) Health(ctx context.Context) (driversdk.HealthStatus, map[string]string) {
  return driversdk.HealthOK, map[string]string{"ip": d.cfg.IP}
}

func (d *ExampleHubDriver) Stop(ctx context.Context) error {
  d.stopOnce.Do(func(){ close(d.stopCh) })
  done := make(chan struct{})
  go func(){ d.wg.Wait(); close(done) }()
  select {
  case <-ctx.Done(): return ctx.Err()
  case <-done: return nil
  }
}

// Hub interface
func (d *ExampleHubDriver) DiscoverChildren(ctx context.Context) ([]driversdk.ChildCandidate, error) {
  // Demo: return a single fake child device.
  return []driversdk.ChildCandidate{
    {
      Protocol: driversdk.ProtocolZigbee,
      ChildRef: "child-001",
      Manufacturer: "Demo",
      Model: "DemoChild",
      Firmware: "1.0",
    },
  }, nil
}


func (d *ExampleHubDriver) ProxyCommand(ctx context.Context, childRef string, endpointKey string, payload []byte) (driversdk.CommandResult, error) {
  // Demo: echo the request back as response data.
  data, _ := json.Marshal(map[string]any{
    "child_ref": childRef,
    "endpoint_key": endpointKey,
    "payload": string(payload),
  })
  return driversdk.CommandResult{Success: true, Message: "ok", Data: data}, nil
}



func (d *ExampleHubDriver) startHubGatewaySession() {
  if d.hostGRPCAddr == "" {
    d.deps.Logger.Warn("NX_HOST_GRPC_ADDR not set; hub proxying disabled")
    return
  }
  conn, err := grpc.Dial(d.hostGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
  if err != nil {
    d.deps.Logger.Error("failed to dial host grpc", "err", err.Error())
    return
  }
  d.grpcConn = conn
  client := hostrpc.NewHubGatewayServiceClient(conn)
  stream, err := client.OpenHubSession(context.Background())
  if err != nil {
    d.deps.Logger.Error("failed to open hub session", "err", err.Error())
    return
  }
  d.hubStream = stream

  // Register hub session
  reg := &hostrpc.ProxyCommandResponse{
    Success: true,
    Message: "register",
    CorrelationId: "reg-" + time.Now().Format("150405.000"),
    HubDeviceId: d.deviceID,
    Kind: "REGISTER",
  }
  if err := stream.Send(reg); err != nil {
    d.deps.Logger.Error("failed to register hub session", "err", err.Error())
    return
  }
  d.deps.Logger.Info("hub session registered", "hub_device_id", d.deviceID)

  // Serve requests from host
  for {
    req, err := stream.Recv()
    if err != nil {
      d.deps.Logger.Error("hub session recv error", "err", err.Error())
      return
    }
    // Execute proxy command using the driver implementation
    res, err := d.ProxyCommand(context.Background(), req.ChildRef, req.EndpointKey, req.Payload)
    resp := &hostrpc.ProxyCommandResponse{
      Success: res.Success,
      Message: res.Message,
      Data:    res.Data,
      CorrelationId: req.CorrelationId,
    }
    if err != nil && resp.Message == "" {
      resp.Success = false
      resp.Message = err.Error()
    }
    if sendErr := stream.Send(resp); sendErr != nil {
      d.deps.Logger.Error("hub session send error", "err", sendErr.Error())
      return
    }
  }
}
