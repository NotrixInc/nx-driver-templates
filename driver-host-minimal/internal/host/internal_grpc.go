package host

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/NotrixInc/nx-driver-sdk/hostrpc"
	"google.golang.org/grpc"
)

// InternalGRPCServer hosts the CHILD->HOST->HUB proxy router.
// - CHILD drivers call HostProxyService.ProxyCommand.
// - HUB drivers open HubGatewayService.OpenHubSession and keep it open.
// The host routes requests to the appropriate hub session by hub device_id.
type InternalGRPCServer struct {
	socketPath string

	grpcServer *ghostrpc.Server
	listener   net.Listener

	hubsMu sync.RWMutex
	hubs   map[string]*hubSession // hubDeviceID -> session
}

type hubSession struct {
	hubDeviceID string
	stream hostrpc.HubGatewayService_OpenHubSessionServer

	// correlation_id -> response channel
	pendingMu sync.Mutex
	pending   map[string]chan *hostrpc.ProxyCommandResponse
}

func NewInternalGRPCServer(socketPath string) *InternalGRPCServer {
	return &InternalGRPCServer{
		socketPath: socketPath,
		hubs:       map[string]*hubSession{},
	}
}

func (s *InternalGRPCServer) Start() error {
	// Ensure directory exists and remove stale socket
	if err := os.MkdirAll(filepath.Dir(s.socketPath), 0o755); err != nil {
		return err
	}
	_ = os.Remove(s.socketPath)

	lis, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return err
	}
	s.listener = lis
	s.grpcServer = ghostrpc.NewServer()

	hostrpc.RegisterHostProxyServiceServer(s.grpcServer, &hostProxyService{server: s})
	hostrpc.RegisterHubGatewayServiceServer(s.grpcServer, &hubGatewayService{server: s})

	go func() {
		_ = s.grpcServer.Serve(lis)
	}()
	return nil
}

func (s *InternalGRPCServer) Stop() {
	if s.grpcServer != nil {
		s.grpcServer.GracefulStop()
	}
	if s.listener != nil {
		_ = s.listener.Close()
	}
	_ = os.Remove(s.socketPath)
}

func (s *InternalGRPCServer) registerHubSession(hubDeviceID string, stream hostrpc.HubGatewayService_OpenHubSessionServer) *hubSession {
	hs := &hubSession{
		hubDeviceID: hubDeviceID,
		stream:      stream,
		pending:     map[string]chan *hostrpc.ProxyCommandResponse{},
	}
	s.hubsMu.Lock()
	s.hubs[hubDeviceID] = hs
	s.hubsMu.Unlock()
	return hs
}

func (s *InternalGRPCServer) unregisterHubSession(hubDeviceID string) {
	s.hubsMu.Lock()
	delete(s.hubs, hubDeviceID)
	s.hubsMu.Unlock()
}

func (s *InternalGRPCServer) getHub(hubDeviceID string) (*hubSession, bool) {
	s.hubsMu.RLock()
	defer s.hubsMu.RUnlock()
	hs, ok := s.hubs[hubDeviceID]
	return hs, ok
}

// Called by HostProxyService to route a command to a hub session and await response.
func (s *InternalGRPCServer) proxyToHub(ctx context.Context, req *hostrpc.ProxyCommandRequest) (*hostrpc.ProxyCommandResponse, error) {
	hs, ok := s.getHub(req.HubDeviceId)
	if !ok {
		return &hostrpc.ProxyCommandResponse{Success: false, Message: "hub not connected", CorrelationId: req.CorrelationId}, errors.New("hub not connected")
	}

	respCh := make(chan *hostrpc.ProxyCommandResponse, 1)
	hs.pendingMu.Lock()
	hs.pending[req.CorrelationId] = respCh
	hs.pendingMu.Unlock()

	// Send request to hub over stream
	if err := hs.stream.Send(req); err != nil {
		hs.pendingMu.Lock()
		delete(hs.pending, req.CorrelationId)
		hs.pendingMu.Unlock()
		return &hostrpc.ProxyCommandResponse{Success: false, Message: "failed to send to hub", CorrelationId: req.CorrelationId}, err
	}

	// Wait for response or timeout
	timeout := 5 * time.Second
	if dl, ok := ctx.Deadline(); ok {
		rem := time.Until(dl)
		if rem > 0 && rem < timeout {
			timeout = rem
		}
	}
	select {
	case <-ctx.Done():
		return &hostrpc.ProxyCommandResponse{Success: false, Message: "context cancelled", CorrelationId: req.CorrelationId}, ctx.Err()
	case resp := <-respCh:
		return resp, nil
	case <-time.After(timeout):
		return &hostrpc.ProxyCommandResponse{Success: false, Message: "hub timeout", CorrelationId: req.CorrelationId}, errors.New("hub timeout")
	}
}

// Hub stream receiver loop: dispatches responses to pending correlation IDs
func (hs *hubSession) recvLoop(ctx context.Context) error {
	for {
		resp, err := hs.stream.Recv()
		if err != nil {
			return err
		}
		hs.pendingMu.Lock()
		ch, ok := hs.pending[resp.CorrelationId]
		if ok {
			delete(hs.pending, resp.CorrelationId)
		}
		hs.pendingMu.Unlock()
		if ok {
			ch <- resp
			close(ch)
		}
	}
}

// --- gRPC service implementations ---

type hostProxyService struct {
	hostrpc.UnimplementedHostProxyServiceServer
	server *InternalGRPCServer
}

func (h *hostProxyService) ProxyCommand(ctx context.Context, req *hostrpc.ProxyCommandRequest) (*hostrpc.ProxyCommandResponse, error) {
	return h.server.proxyToHub(ctx, req)
}

type hubGatewayService struct {
	hostrpc.UnimplementedHubGatewayServiceServer
	server *InternalGRPCServer
}

func (g *hubGatewayService) OpenHubSession(stream hostrpc.HubGatewayService_OpenHubSessionServer) error {
	// First message from hub MUST be a registration response (client->server stream):
// The hub sends a ProxyCommandResponse with Kind="REGISTER" and HubDeviceId set.
first, err := stream.Recv()
if err != nil {
    return err
}
if first.Kind != "REGISTER" || first.HubDeviceId == "" {
    return errors.New("hub must register first: ProxyCommandResponse{Kind:'REGISTER', HubDeviceId:<uuid>}")
}

hs := g.server.registerHubSession(first.HubDeviceId, stream)
defer g.server.unregisterHubSession(first.HubDeviceId)

// Start receiver loop to accept responses from hub.
 to accept responses from hub.
	return hs.recvLoop(context.Background())
}
