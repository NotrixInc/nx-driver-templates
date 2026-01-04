package host

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type Proc struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	stdout io.ReadCloser
	stderr io.ReadCloser
}

type StartSpec struct {
	BinaryPath string
	DeviceID   string
	ConfigJSON []byte
	WorkDir    string
	Env        []string
}

func (p *Proc) Start(ctx context.Context, s StartSpec) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cmd != nil {
		return fmt.Errorf("process already started")
	}

	if err := os.MkdirAll(s.WorkDir, 0o755); err != nil {
		return err
	}
	cfgPath := filepath.Join(s.WorkDir, "config.json")
	if err := os.WriteFile(cfgPath, s.ConfigJSON, 0o600); err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, s.BinaryPath, "-device_id", s.DeviceID, "-config", cfgPath)
	cmd.Dir = s.WorkDir
	cmd.Env = append(os.Environ(), s.Env...)

	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	p.stdout = stdout
	p.stderr = stderr

	if err := cmd.Start(); err != nil {
		return err
	}
	p.cmd = cmd
	return nil
}

func (p *Proc) Wait() error {
	p.mu.Lock()
	cmd := p.cmd
	p.mu.Unlock()
	if cmd == nil {
		return nil
	}
	return cmd.Wait()
}

func (p *Proc) Stop(timeout time.Duration) error {
	p.mu.Lock()
	cmd := p.cmd
	p.mu.Unlock()
	if cmd == nil {
		return nil
	}
	// CmdContext will send SIGKILL on context cancel; here we try graceful interrupt first.
	_ = cmd.Process.Signal(os.Interrupt)
	t := time.NewTimer(timeout)
	defer t.Stop()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-t.C:
		_ = cmd.Process.Kill()
		return fmt.Errorf("killed after timeout")
	}
}

func (p *Proc) Stdout() io.ReadCloser { return p.stdout }
func (p *Proc) Stderr() io.ReadCloser { return p.stderr }
