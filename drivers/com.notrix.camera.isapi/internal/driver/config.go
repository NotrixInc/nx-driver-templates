package driver

type Config struct {
	IP                     string `json:"ip"`
	SnapshotPort           int    `json:"snapshot_port"`
	StreamPort             int    `json:"stream_port"`
	ChannelNumber          int    `json:"channel_number"`
	StreamType             string `json:"stream_type"`
	Username               string `json:"username"`
	Password               string `json:"password"`
	AuthType               string `json:"auth_type"`
	SnapshotResolution     string `json:"snapshot_resolution"`
	SnapshotRefreshMs      int    `json:"snapshot_refresh_ms"`
	EmbedCredentialsInRTSP bool   `json:"embed_credentials_in_rtsp_url"`
	RequestTimeoutSeconds  int    `json:"request_timeout_seconds"`

	// PTZ says the camera can pan, tilt and zoom. ISAPI cameras do not
	// advertise it reliably, so the installer says so.
	PTZ bool `json:"ptz"`
	// MotionEvents says the camera reports motion (cap.camera@v1 caps).
	MotionEvents bool `json:"motion_events"`
	// KeepWarm asks the controller to keep this camera's sub stream connected
	// so a tap starts in well under a second. Read by core, not the driver.
	KeepWarm bool `json:"keep_warm"`
}
