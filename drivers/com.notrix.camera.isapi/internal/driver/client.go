package driver

import (
	"context"
	"crypto/md5"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	ip             string
	snapshotPort   int
	streamPort     int
	channelNumber  int
	streamType     string
	username       string
	password       string
	authType       string
	snapshotRes    string
	requestTimeout time.Duration
	http           *http.Client
}

func NewClient(cfg Config) *Client {
	snapshotPort := cfg.SnapshotPort
	if snapshotPort == 0 {
		snapshotPort = 80
	}
	streamPort := cfg.StreamPort
	if streamPort == 0 {
		streamPort = 554
	}
	channelNumber := cfg.ChannelNumber
	if channelNumber <= 0 {
		channelNumber = 1
	}
	streamType := strings.ToLower(strings.TrimSpace(cfg.StreamType))
	if streamType == "" {
		streamType = "sub"
	}
	if streamType != "main" && streamType != "sub" {
		streamType = "sub"
	}
	res := strings.TrimSpace(cfg.SnapshotResolution)
	if res == "" {
		res = "640x480"
	}
	toSec := cfg.RequestTimeoutSeconds
	if toSec <= 0 {
		toSec = 8
	}
	authType := strings.ToLower(strings.TrimSpace(cfg.AuthType))
	if authType == "" {
		authType = "basic"
	}

	timeout := time.Duration(toSec) * time.Second

	// Custom transport: shorter dial timeout so unreachable NVR/cameras
	// fail fast instead of blocking the full request timeout.
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   3 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          4,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: timeout,
	}

	return &Client{
		ip:             strings.TrimSpace(cfg.IP),
		snapshotPort:   snapshotPort,
		streamPort:     streamPort,
		channelNumber:  channelNumber,
		streamType:     streamType,
		username:       cfg.Username,
		password:       cfg.Password,
		authType:       authType,
		snapshotRes:    res,
		requestTimeout: timeout,
		http:           &http.Client{Timeout: timeout, Transport: transport},
	}
}

func (c *Client) SnapshotURL() string {
	host := net.JoinHostPort(c.ip, fmt.Sprintf("%d", c.snapshotPort))
	u := url.URL{
		Scheme: "http",
		Host:   host,
		Path:   fmt.Sprintf("/ISAPI/Streaming/channels/%d/picture", c.channelNumber),
	}
	q := u.Query()
	q.Set("resolution", c.snapshotRes)
	u.RawQuery = q.Encode()
	return u.String()
}

func (c *Client) StreamURL(embedCreds bool) string {
	host := net.JoinHostPort(c.ip, fmt.Sprintf("%d", c.streamPort))
	u := url.URL{
		Scheme: "rtsp",
		Host:   host,
		Path:   fmt.Sprintf("/Streaming/channels/%d", c.rtspChannelCode()),
	}
	if embedCreds {
		u.User = url.UserPassword(c.username, c.password)
	}
	return u.String()
}

func (c *Client) rtspChannelCode() int {
	streamVariant := 2
	if c.streamType == "main" {
		streamVariant = 1
	}
	return c.channelNumber*100 + streamVariant
}

func (c *Client) FetchSnapshot(ctx context.Context) ([]byte, string, error) {
	// Use the caller-provided context directly; it already carries the
	// appropriate deadline from publishSnapshot. Adding a second timeout
	// here used to double the maximum wait time.

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.SnapshotURL(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "image/jpeg,image/*;q=0.9,*/*;q=0.8")

	requestURI := req.URL.RequestURI()

	// Try request with auth
	if c.authType == "basic" {
		req.SetBasicAuth(c.username, c.password)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	// If digest auth (or basic fallback) and got 401, retry with digest
	if resp.StatusCode == 401 {
		wwwAuth := resp.Header.Get("WWW-Authenticate")
		if strings.HasPrefix(strings.ToLower(wwwAuth), "digest") {
			if c.authType != "digest" && c.authType != "basic" {
				// Unknown auth mode, don't attempt digest.
			} else {
				// Parse digest challenge and retry
				authHeader, err := c.buildDigestAuthHeader("GET", requestURI, wwwAuth)
				if err != nil {
					return nil, "", fmt.Errorf("digest auth failed: %w", err)
				}

				req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.SnapshotURL(), nil)
				req2.Header.Set("Accept", "image/jpeg,image/*;q=0.9,*/*;q=0.8")
				req2.Header.Set("Authorization", authHeader)

				resp, err = c.http.Do(req2)
				if err != nil {
					return nil, "", err
				}
				defer resp.Body.Close()
			}
		}
	}

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		trim := string(body)
		if len(trim) > 300 {
			trim = trim[:300]
		}
		return nil, "", fmt.Errorf("snapshot request failed: %s: %s", resp.Status, strings.TrimSpace(trim))
	}

	ct := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if ct == "" {
		ct = "image/jpeg"
	}
	return body, ct, nil
}

func (c *Client) buildDigestAuthHeader(method, uri, wwwAuth string) (string, error) {
	// Parse WWW-Authenticate header
	parts := make(map[string]string)
	authParts := strings.Split(strings.TrimPrefix(wwwAuth, "Digest "), ",")
	for _, part := range authParts {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 {
			key := strings.TrimSpace(kv[0])
			val := strings.Trim(strings.TrimSpace(kv[1]), "\"")
			parts[key] = val
		}
	}

	realm := parts["realm"]
	nonce := parts["nonce"]
	qop := parts["qop"]
	opaque := parts["opaque"]

	// Build digest response
	ha1 := md5Hash(fmt.Sprintf("%s:%s:%s", c.username, realm, c.password))
	ha2 := md5Hash(fmt.Sprintf("%s:%s", method, uri))

	var response string
	nc := "00000001"
	cnonce := "0a4f113b"

	if qop == "auth" || qop == "auth-int" {
		response = md5Hash(fmt.Sprintf("%s:%s:%s:%s:%s:%s", ha1, nonce, nc, cnonce, qop, ha2))
	} else {
		response = md5Hash(fmt.Sprintf("%s:%s:%s", ha1, nonce, ha2))
	}

	// Build Authorization header
	auth := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s"`,
		c.username, realm, nonce, uri, response)

	if qop != "" {
		auth += fmt.Sprintf(`, qop=%s, nc=%s, cnonce="%s"`, qop, nc, cnonce)
	}
	if opaque != "" {
		auth += fmt.Sprintf(`, opaque="%s"`, opaque)
	}

	return auth, nil
}

func md5Hash(text string) string {
	h := md5.New()
	h.Write([]byte(text))
	return fmt.Sprintf("%x", h.Sum(nil))
}

// ── cap.camera@v1 (CAMERA-STREAMING.md §4.1) ───────────────────────────

// MainStreamURL and SubStreamURL are the descriptor's streams. Never with
// credentials: the controller resolves those from its own secret store.
func (c *Client) MainStreamURL() string { return c.rtspURL(c.channelNumber*100 + 1) }
func (c *Client) SubStreamURL() string  { return c.rtspURL(c.channelNumber*100 + 2) }

func (c *Client) rtspURL(code int) string {
	u := url.URL{Scheme: "rtsp", Host: net.JoinHostPort(c.ip, fmt.Sprintf("%d", c.streamPort)), Path: fmt.Sprintf("/Streaming/Channels/%d", code)}
	return u.String()
}

// DescriptorSnapshotURL is the sub stream's still image: the camera scales it
// for us, so the controller decodes nothing for a grid tile.
func (c *Client) DescriptorSnapshotURL() string {
	u := url.URL{Scheme: "http", Host: c.httpHost(), Path: fmt.Sprintf("/ISAPI/Streaming/channels/%d/picture", c.channelNumber*100+2)}
	return u.String()
}

func (c *Client) httpHost() string {
	if c.snapshotPort == 80 {
		return c.ip
	}
	return net.JoinHostPort(c.ip, fmt.Sprintf("%d", c.snapshotPort))
}

// AuthType is what the descriptor tells the controller to answer with.
func (c *Client) AuthType() string { return c.authType }

// isapi sends one ISAPI request, answering a digest challenge.
func (c *Client) isapi(ctx context.Context, method, path, body string) error {
	target := "http://" + c.httpHost() + path
	send := func(authz string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(body))
		if err != nil {
			return nil, err
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/xml")
		}
		if authz != "" {
			req.Header.Set("Authorization", authz)
		} else if c.authType == "basic" {
			req.SetBasicAuth(c.username, c.password)
		}
		return c.http.Do(req)
	}
	resp, err := send("")
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		chal := resp.Header.Get("WWW-Authenticate")
		resp.Body.Close()
		if !strings.HasPrefix(strings.ToLower(chal), "digest") {
			return fmt.Errorf("isapi %s %s: unauthorized", method, path)
		}
		authz, err := c.buildDigestAuthHeader(method, path, chal)
		if err != nil {
			return err
		}
		if resp, err = send(authz); err != nil {
			return err
		}
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("isapi %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

func clampPTZ(v float64) int {
	if v > 100 {
		v = 100
	}
	if v < -100 {
		v = -100
	}
	return int(v)
}

// PTZContinuous starts a move at the given speeds (-100..100). A zero vector
// stops. Moves are RELATIVE in bus terms (§20.6): a speed, not a position.
func (c *Client) PTZContinuous(ctx context.Context, pan, tilt, zoom float64) error {
	body := fmt.Sprintf("<PTZData><pan>%d</pan><tilt>%d</tilt><zoom>%d</zoom></PTZData>", clampPTZ(pan), clampPTZ(tilt), clampPTZ(zoom))
	return c.isapi(ctx, http.MethodPut, fmt.Sprintf("/ISAPI/PTZCtrl/channels/%d/continuous", c.channelNumber), body)
}

// PresetGoto recalls a preset. ABSOLUTE in bus terms.
func (c *Client) PresetGoto(ctx context.Context, preset int) error {
	return c.isapi(ctx, http.MethodPut, fmt.Sprintf("/ISAPI/PTZCtrl/channels/%d/presets/%d/goto", c.channelNumber, preset), "")
}

// PresetSave stores the current position as a preset.
func (c *Client) PresetSave(ctx context.Context, preset int, name string) error {
	if name == "" {
		name = fmt.Sprintf("Preset %d", preset)
	}
	var esc strings.Builder
	_ = xml.EscapeText(&esc, []byte(name))
	body := fmt.Sprintf("<PTZPreset><id>%d</id><presetName>%s</presetName></PTZPreset>", preset, esc.String())
	return c.isapi(ctx, http.MethodPut, fmt.Sprintf("/ISAPI/PTZCtrl/channels/%d/presets/%d", c.channelNumber, preset), body)
}
