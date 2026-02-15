package driver

type Config struct {
	BaseURL               string `json:"base_url"`
	StatusPath            string `json:"status_path"`
	SetPath               string `json:"set_path"`
	Username              string `json:"username"`
	Password              string `json:"password"`
	AuthType              string `json:"auth_type"`
	BearerToken           string `json:"bearer_token"`
	PollIntervalMs        int    `json:"poll_interval_ms"`
	RequestTimeoutSeconds int    `json:"request_timeout_seconds"`
	InsecureSkipTLSVerify bool   `json:"insecure_skip_tls_verify"`
}
