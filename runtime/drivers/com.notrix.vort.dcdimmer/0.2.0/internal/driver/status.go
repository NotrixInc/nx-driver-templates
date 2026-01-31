package driver

import "strings"

func statusString(s Status, key string) string {
	v, ok := s[key]
	if !ok || v == nil {
		return ""
	}
	if str, ok := v.(string); ok {
		return strings.TrimSpace(str)
	}
	return ""
}

func statusBool(s Status, key string) (bool, bool) {
	v, ok := s[key]
	if !ok || v == nil {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

func statusFloat(s Status, key string) (float64, bool) {
	v, ok := s[key]
	if !ok || v == nil {
		return 0, false
	}
	// encoding/json unmarshals numbers as float64 into map[string]any
	f, ok := v.(float64)
	return f, ok
}
