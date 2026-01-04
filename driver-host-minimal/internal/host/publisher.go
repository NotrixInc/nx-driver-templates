package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/yourorg/controller-platform/pkg/driversdk"
)

// PublisherMode supports dev flows until you wire gRPC CoreService.
type PublisherMode string

const (
	PublisherNoop PublisherMode = "noop"
	PublisherFile PublisherMode = "file"
)

type FilePublisher struct {
	mu  sync.Mutex
	dir string
}

func NewFilePublisher(dir string) *FilePublisher {
	return &FilePublisher{dir: dir}
}

func (p *FilePublisher) ensure() error {
	return os.MkdirAll(p.dir, 0o755)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func (p *FilePublisher) UpsertDevice(ctx context.Context, d driversdk.DeviceDescriptor) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensure(); err != nil { return err }
	return writeJSON(filepath.Join(p.dir, "device_"+sanitize(d.DeviceID)+".json"), d)
}

func (p *FilePublisher) UpsertEndpoints(ctx context.Context, deviceID string, eps []driversdk.Endpoint) error {
	p.mu.Lock(); defer p.mu.Unlock()
	if err := p.ensure(); err != nil { return err }
	return writeJSON(filepath.Join(p.dir, "endpoints_"+sanitize(deviceID)+".json"), eps)
}

func (p *FilePublisher) UpsertVariables(ctx context.Context, deviceID string, vars []driversdk.Variable) error {
	p.mu.Lock(); defer p.mu.Unlock()
	if err := p.ensure(); err != nil { return err }
	return writeJSON(filepath.Join(p.dir, "variables_"+sanitize(deviceID)+".json"), vars)
}

func (p *FilePublisher) PublishState(ctx context.Context, s driversdk.StateUpdate) error {
	p.mu.Lock(); defer p.mu.Unlock()
	if err := p.ensure(); err != nil { return err }
	return writeJSON(filepath.Join(p.dir, "state_"+sanitize(s.DeviceID)+".json"), s)
}

func (p *FilePublisher) PublishVariable(ctx context.Context, v driversdk.VariableUpdate) error {
	p.mu.Lock(); defer p.mu.Unlock()
	if err := p.ensure(); err != nil { return err }
	return writeJSON(filepath.Join(p.dir, "var_"+sanitize(v.DeviceID)+"_"+sanitize(v.Key)+".json"), v)
}

func (p *FilePublisher) PublishEvent(ctx context.Context, e driversdk.DeviceEvent) error {
	p.mu.Lock(); defer p.mu.Unlock()
	if err := p.ensure(); err != nil { return err }
	// append-like behavior by timestamp suffix
	name := "event_" + sanitize(e.DeviceID) + "_" + sanitize(e.Type) + "_" + sanitize(e.At.Format("20060102T150405.000Z0700")) + ".json"
	return writeJSON(filepath.Join(p.dir, name), e)
}

func sanitize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			out = append(out, r)
		case r >= 'A' && r <= 'Z':
			out = append(out, r)
		case r >= '0' && r <= '9':
			out = append(out, r)
		case r == '-' || r == '_' || r == '.':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}
