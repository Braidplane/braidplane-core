package run

import (
	"strings"
	"time"
)

// HTTPWatermarkTime parses HTTP progress without silently losing sub-nanosecond precision.
func HTTPWatermarkTime(value any) (time.Time, bool) {
	timestamp, ok := value.(string)
	// Go accepts single-digit hours and out-of-range timezone offsets.
	if !ok || len(timestamp) < 20 || timestamp[13] != ':' || strings.Contains(timestamp, ",") {
		return time.Time{}, false
	}
	if timestamp[len(timestamp)-1] != 'Z' {
		zone := timestamp[len(timestamp)-6:]
		if zone[1:3] > "23" || zone[4:] > "59" {
			return time.Time{}, false
		}
	}
	// Go truncates sub-nanosecond fractions; reject them to preserve progress precision.
	if len(timestamp) > 19 && timestamp[19] == '.' {
		fraction := strings.TrimLeft(timestamp[20:], "0123456789")
		if len(timestamp[20:])-len(fraction) > 9 {
			return time.Time{}, false
		}
	}
	parsed, err := time.Parse(time.RFC3339Nano, timestamp)
	return parsed, err == nil
}
