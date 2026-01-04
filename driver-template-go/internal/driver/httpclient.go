package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
	"bytes"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func NewClient(ip string, port int, token string) *Client {
	if port == 0 {
		port = 80
	}
	return &Client{
		baseURL: fmt.Sprintf("http://%s:%d", ip, port),
		token:   token,
		http:   &http.Client{Timeout: 5 * time.Second},
	}
}

// Example device API endpoints (replace with real device protocol)
func (c *Client) GetPower(ctx context.Context) (bool, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/power", nil)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	var out struct {
		On bool `json:"on"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, err
	}
	return out.On, nil
}

func (c *Client) SetPower(ctx context.Context, on bool) error {
	body, _ := json.Marshal(map[string]any{"on": on})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/power", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("set power failed: %s", resp.Status)
	}
	return nil
}

// small helper (avoid importing bytes in multiple files if you prefer)
func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
