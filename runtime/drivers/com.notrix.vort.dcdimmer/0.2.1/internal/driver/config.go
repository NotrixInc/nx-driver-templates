package driver

type Config struct {
	IP             string `json:"ip"`
	Port           int    `json:"port"`
	Username       string `json:"username"`
	Password       string `json:"password"`
	PollIntervalMs int    `json:"poll_interval_ms"`
	Channels       int    `json:"channels"`

	MQTTEnable   bool   `json:"mqtt_enable"`
	MQTTUsername string `json:"mqtt_username"`
	MQTTPassword string `json:"mqtt_password"`
	MQTTTopic    string `json:"mqtt_topic"`
}
