package config

import "encoding/json"

type DriverType string

const (
	DriverTypeDevice DriverType = "DEVICE"
	DriverTypeHub    DriverType = "HUB"
	DriverTypeChild  DriverType = "CHILD"
)

type InstanceSpec struct {
	InstanceID string          `json:"instance_id"`
	DeviceID   string          `json:"device_id"`
	DriverID   string          `json:"driver_id"`
	DriverType DriverType      `json:"driver_type"`
	Binary     string          `json:"binary"`
	Config     json.RawMessage `json:"config"`
}

type InstancesFile struct {
	Instances []InstanceSpec `json:"instances"`
}
