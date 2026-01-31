package driver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Client struct {
	baseURL string
	auth    string
	http    *http.Client

	mu            sync.RWMutex
	dimmingURLFmt string
}

func NewClient(ip string, port int, username, password string) *Client {
	if port == 0 {
		port = 80
	}
	if strings.TrimSpace(username) == "" {
		username = "Notrix"
	}
	if strings.TrimSpace(password) == "" {
		password = "Notrix"
	}
	return &Client{
		baseURL: fmt.Sprintf("http://%s:%d", ip, port),
		auth:    "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password)),
		// Slightly higher timeout to avoid intermittent poll failures on a busy/slow device.
		http: &http.Client{Timeout: 10 * time.Second},
	}
}

type Status map[string]any

func (c *Client) addAuth(req *http.Request) {
	if c.auth != "" {
		req.Header.Set("Authorization", c.auth)
	}
}

func (c *Client) GetStatus(ctx context.Context) (Status, error) {
	return c.GetBestStatus(ctx, 10)
}

// GetBestStatus prefers full status polling from /api (so telemetry fields populate),
// and falls back to per-channel dimming polling if /api is unavailable.
func (c *Client) GetBestStatus(ctx context.Context, channels int) (Status, error) {
	// Try full status with a bounded sub-timeout so fallback still has time.
	fullCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	if st, err := c.GetFullStatus(fullCtx); err == nil {
		return st, nil
	}
	return c.GetStatusForChannels(ctx, channels)
}

// GetFullStatus fetches the full device status JSON.
// This endpoint includes telemetry fields like voltages, currents, energy, device metadata,
// and also channel{N}DimmingValue values.
func (c *Client) GetFullStatus(ctx context.Context) (Status, error) {
	raw, err := c.getJSON(ctx, "/api")
	if err != nil {
		return nil, err
	}
	if m, ok := raw.(map[string]any); ok {
		return Status(m), nil
	}
	// Unexpected shape; normalize best-effort.
	return normalizeChannelStatus(raw), nil
}

// GetStatusForChannels polls dimming values using the device's channel endpoint.
// The device API is expected to be:
//
//	http://<ip>:<port>/api/channel?DimmingValue<channel>
//
// or one of several common query variants.
//
// We auto-detect the exact query format on first success and cache it.
func (c *Client) GetStatusForChannels(ctx context.Context, channels int) (Status, error) {
	if channels <= 0 {
		channels = 10
	}
	if channels > 10 {
		channels = 10
	}

	out := Status{}
	var outMu sync.Mutex

	// Poll each channel concurrently (bounded), so we don't block the whole tick.
	// Keep concurrency low; some devices rate-limit or get flaky under parallel load.
	sem := make(chan struct{}, 2)
	var wg sync.WaitGroup
	var firstErr error
	var firstErrMu sync.Mutex

	for ch := 1; ch <= channels; ch++ {
		ch := ch
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-ctx.Done():
				firstErrMu.Lock()
				if firstErr == nil {
					firstErr = ctx.Err()
				}
				firstErrMu.Unlock()
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()

			v, err := c.getChannelDimmingValue(ctx, ch)
			if err != nil {
				firstErrMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				firstErrMu.Unlock()
				return
			}

			outMu.Lock()
			out[fmt.Sprintf("channel%dDimmingValue", ch)] = v
			outMu.Unlock()
		}()
	}

	wg.Wait()
	if len(out) == 0 {
		if firstErr != nil {
			return nil, firstErr
		}
		return nil, fmt.Errorf("poll failed: no channel values returned")
	}
	return out, nil
}

func (c *Client) getChannelDimmingValue(ctx context.Context, channel int) (float64, error) {
	if channel < 1 || channel > 10 {
		return 0, fmt.Errorf("invalid channel: %d", channel)
	}

	// Primary / expected schema (per device spec):
	//   GET /api/channel{N}DimmingValue
	// Example:
	//   http://192.168.7.208:80/api/channel1DimmingValue
	if raw, err := c.getJSON(ctx, fmt.Sprintf("/api/channel%dDimmingValue", channel)); err == nil {
		if v, ok := extractDimmingValue(raw, channel); ok {
			return v, nil
		}
	}

	// If we already learned the working URL format, try it first.
	c.mu.RLock()
	fmtCached := c.dimmingURLFmt
	c.mu.RUnlock()
	if fmtCached != "" {
		raw, err := c.getJSON(ctx, fmt.Sprintf(fmtCached, channel))
		if err == nil {
			if v, ok := extractDimmingValue(raw, channel); ok {
				return v, nil
			}
			// If the cached format returns a shape we don't understand, fall back to probing.
		}
	}

	// Probe common variants once, then cache the first that works.
	// IMPORTANT: keep this restricted to the /api namespace to avoid
	// accidentally caching /ap/* routes (which are 404 on this firmware).
	candidates := []string{
		"/api/channel%dDimmingValue",
		"/api/channel/%d/DimmingValue",
		"/api/channel?DimmingValue=%d",
		"/api/channel?dimmingValue=%d",
		"/api/channel?channel=%d&DimmingValue",
		"/api/channel?ch=%d&DimmingValue",
	}

	var lastErr error
	for _, fmtStr := range candidates {
		path := fmt.Sprintf(fmtStr, channel)
		raw, err := c.getJSON(ctx, path)
		if err != nil {
			lastErr = err
			continue
		}
		v, ok := extractDimmingValue(raw, channel)
		if !ok {
			lastErr = fmt.Errorf("unexpected response for %s", path)
			continue
		}

		c.mu.Lock()
		c.dimmingURLFmt = fmtStr
		c.mu.Unlock()
		return v, nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no working channel query format")
	}
	return 0, lastErr
}

func (c *Client) getJSON(ctx context.Context, path string) (any, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	c.addAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("get %s failed: %s: %s", path, resp.Status, strings.TrimSpace(string(b)))
	}
	var raw any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func extractDimmingValue(raw any, channel int) (float64, bool) {
	// Direct map: {"DimmingValue": 50}
	if m, ok := raw.(map[string]any); ok {
		if v, ok := readNumberAny(m, "DimmingValue", "dimmingValue"); ok {
			return v, true
		}
		key := fmt.Sprintf("channel%dDimmingValue", channel)
		if v, ok := m[key]; ok {
			return parseNumberAny(v)
		}
	}

	// If response is complex, normalize to our Status shape and read from it.
	st := normalizeChannelStatus(raw)
	key := fmt.Sprintf("channel%dDimmingValue", channel)
	if v, ok := st[key]; ok {
		return parseNumberAny(v)
	}
	return 0, false
}

func normalizeChannelStatus(raw any) Status {
	out := Status{}

	// If it's already a map, keep it and also try to interpret nested shapes.
	if m, ok := raw.(map[string]any); ok {
		for k, v := range m {
			out[k] = v
		}

		// Common nesting: {"channels": [ ... ]}
		if chs, ok := m["channels"]; ok {
			mergeChannelList(out, chs)
		}
		if chs, ok := m["channel"]; ok {
			mergeChannelList(out, chs)
		}

		// Common nesting: {"DimmingValue": {"1": 50, "2": 0}} or array
		if dv, ok := m["DimmingValue"]; ok {
			mergeDimmingValueContainer(out, dv)
		}
		if dv, ok := m["dimmingValue"]; ok {
			mergeDimmingValueContainer(out, dv)
		}

		return out
	}

	// Or it could be a plain list of channels.
	mergeChannelList(out, raw)
	return out
}

func mergeChannelList(out Status, raw any) {
	arr, ok := raw.([]any)
	if !ok {
		return
	}
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		ch, ok := readChannelNumber(m)
		if !ok || ch < 1 || ch > 10 {
			continue
		}
		if v, ok := readNumberAny(m, "DimmingValue", "dimmingValue"); ok {
			out[fmt.Sprintf("channel%dDimmingValue", ch)] = v
		}
	}
}

func mergeDimmingValueContainer(out Status, raw any) {
	// map like {"1": 50, "2": 0}
	if m, ok := raw.(map[string]any); ok {
		for k, v := range m {
			ch, ok := parseIntAny(k)
			if !ok || ch < 1 || ch > 10 {
				continue
			}
			if n, ok := parseNumberAny(v); ok {
				out[fmt.Sprintf("channel%dDimmingValue", ch)] = n
			}
		}
		return
	}
	// array like [50, 0, 10, ...] where index+1=channel
	if arr, ok := raw.([]any); ok {
		for i, v := range arr {
			ch := i + 1
			if ch < 1 || ch > 10 {
				continue
			}
			if n, ok := parseNumberAny(v); ok {
				out[fmt.Sprintf("channel%dDimmingValue", ch)] = n
			}
		}
	}
}

func readChannelNumber(m map[string]any) (int, bool) {
	// Try common keys for channel ID.
	if n, ok := readNumberAny(m, "Channel", "channel", "ChannelId", "channelId", "id", "Index", "index"); ok {
		ch := int(n)
		return ch, true
	}
	return 0, false
}

func readNumberAny(m map[string]any, keys ...string) (float64, bool) {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if n, ok := parseNumberAny(v); ok {
				return n, true
			}
		}
	}
	return 0, false
}

func parseNumberAny(v any) (float64, bool) {
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
	case uint:
		return float64(t), true
	case uint64:
		return float64(t), true
	case uint32:
		return float64(t), true
	case json.Number:
		n, err := t.Float64()
		return n, err == nil
	case string:
		// Try int first to avoid locale issues.
		if i, ok := parseIntAny(t); ok {
			return float64(i), true
		}
		return 0, false
	default:
		return 0, false
	}
}

func parseIntAny(s string) (int, bool) {
	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n); err != nil {
		return 0, false
	}
	return n, true
}

func (c *Client) PutProperty(ctx context.Context, propertyName string, value any) error {
	propertyName = strings.TrimPrefix(propertyName, "/")
	propertyName = strings.TrimPrefix(propertyName, "api/")
	if propertyName == "" {
		return fmt.Errorf("propertyName required")
	}
	if _, err := url.PathUnescape(propertyName); err != nil {
		return fmt.Errorf("invalid propertyName: %w", err)
	}

	// Device expects object payload, e.g. {"channel1DimmingValue": 50}
	return c.put(ctx, "/api/"+propertyName, map[string]any{propertyName: value})
}

func (c *Client) put(ctx context.Context, path string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	c.addAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("put %s failed: %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}
