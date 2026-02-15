package driver

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

type Client struct {
	baseURL     string
	statusPath  string
	setPath     string
	username    string
	password    string
	authType    string
	bearerToken string
	timeout     time.Duration
	http        *http.Client
}

func NewClient(cfg Config) (*Client, error) {
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		return nil, fmt.Errorf("config.base_url is required")
	}
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "http://" + base
	}
	if _, err := url.Parse(base); err != nil {
		return nil, fmt.Errorf("invalid base_url: %w", err)
	}

	statusPath := strings.TrimSpace(cfg.StatusPath)
	if statusPath == "" {
		statusPath = "/api/status"
	}
	setPath := strings.TrimSpace(cfg.SetPath)
	if setPath == "" {
		setPath = "/api/set"
	}
	timeoutSec := cfg.RequestTimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = 8
	}
	authType := strings.ToLower(strings.TrimSpace(cfg.AuthType))
	if authType == "" {
		authType = "none"
	}

	transport := &http.Transport{}
	if cfg.InsecureSkipTLSVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	return &Client{
		baseURL:     strings.TrimRight(base, "/"),
		statusPath:  statusPath,
		setPath:     setPath,
		username:    cfg.Username,
		password:    cfg.Password,
		authType:    authType,
		bearerToken: cfg.BearerToken,
		timeout:     time.Duration(timeoutSec) * time.Second,
		http:        &http.Client{Timeout: time.Duration(timeoutSec) * time.Second, Transport: transport},
	}, nil
}

func (c *Client) buildURL(p string) string {
	u, _ := url.Parse(c.baseURL)
	u.Path = path.Join(strings.TrimRight(u.Path, "/"), strings.TrimLeft(p, "/"))
	return u.String()
}

func (c *Client) withAuth(req *http.Request) {
	switch c.authType {
	case "basic":
		req.SetBasicAuth(c.username, c.password)
	case "bearer":
		if strings.TrimSpace(c.bearerToken) != "" {
			req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.bearerToken))
		}
	}
}

func (c *Client) GetStatus(ctx context.Context) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.buildURL(c.statusPath), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	c.withAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status request failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode status response: %w", err)
	}
	return out, nil
}

func (c *Client) SetValue(ctx context.Context, key string, value any) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	payload := map[string]any{"key": key, "value": value}
	buf, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.buildURL(c.setPath), bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	c.withAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("set value failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}
