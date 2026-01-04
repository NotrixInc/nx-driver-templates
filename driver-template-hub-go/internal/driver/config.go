package driver

type Config struct {
  IP string `json:"ip"`
  Port int `json:"port"`
  Token string `json:"token"`
  PollIntervalSeconds int `json:"poll_interval_seconds"`
}
