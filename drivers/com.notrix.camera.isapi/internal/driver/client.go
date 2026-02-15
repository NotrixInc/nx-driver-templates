package driver

import (
	"context"
	"crypto/md5"
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
		Path:   fmt.Sprintf("/ISAPI/Streaming/channels/%d/picture", c.rtspChannelCode()),
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
