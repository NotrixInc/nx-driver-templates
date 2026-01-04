package host

import (
	"context"
	"errors"
	"sync"

	"github.com/NotrixInc/nx-driver-sdk"
)

// HubInstance is the minimal callable surface the host needs for routing.
type HubInstance interface {
	DeviceID() string
	ProxyCommand(ctx context.Context, childRef string, endpointKey string, payload []byte) (driversdk.CommandResult, error)
}

type HubRegistry struct {
	mu   sync.RWMutex
	hubs map[string]HubInstance // key: hub device_id
}

func NewHubRegistry() *HubRegistry {
	return &HubRegistry{hubs: map[string]HubInstance{}}
}

func (r *HubRegistry) Register(h HubInstance) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hubs[h.DeviceID()] = h
}

func (r *HubRegistry) Unregister(deviceID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.hubs, deviceID)
}

func (r *HubRegistry) Get(deviceID string) (HubInstance, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.hubs[deviceID]
	return h, ok
}

// HubProxyRouter implements driversdk.HubProxy to be given to CHILD drivers.
type HubProxyRouter struct {
	registry *HubRegistry
	hubID    string
}

func NewHubProxyRouter(reg *HubRegistry, hubDeviceID string) *HubProxyRouter {
	return &HubProxyRouter{registry: reg, hubID: hubDeviceID}
}

func (h *HubProxyRouter) ProxyCommand(ctx context.Context, childRef string, endpointKey string, payload []byte) (driversdk.CommandResult, error) {
	hub, ok := h.registry.Get(h.hubID)
	if !ok {
		return driversdk.CommandResult{Success: false, Message: "hub not running"}, errors.New("hub not running: " + h.hubID)
	}
	return hub.ProxyCommand(ctx, childRef, endpointKey, payload)
}
