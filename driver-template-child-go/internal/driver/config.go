package driver

type Config struct {
	HubDeviceID         string `json:"hub_device_id"`
	ChildRef            string `json:"child_ref"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
}
