package config

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// Duration is a config-native duration value that parses consistently from TOML, env vars,
// and command-line flags using Go duration syntax such as "30s" or "1m".
type Duration time.Duration

func NewDuration(value time.Duration) Duration {
	return Duration(value)
}

func (d Duration) TimeDuration() time.Duration {
	return time.Duration(d)
}

func (d Duration) Seconds() float64 {
	return time.Duration(d).Seconds()
}

func (d Duration) Milliseconds() int64 {
	return time.Duration(d).Milliseconds()
}

func (d Duration) String() string {
	return time.Duration(d).String()
}

func (d *Duration) Set(value string) error {
	return d.UnmarshalText([]byte(value))
}

func (d *Duration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(string(text))
	if err != nil {
		return xerror.InvalidInput(xerror.JSON).Detailf("parse duration %q: %w", string(text), err)
	}
	*d = Duration(parsed)
	return nil
}

func (d *Duration) UnmarshalTOML(value any) error {
	switch typed := value.(type) {
	case string:
		return d.UnmarshalText([]byte(typed))
	case time.Duration:
		*d = Duration(typed)
		return nil
	default:
		return xerror.ValidationFailed().Detailf("duration must be a string, got %T", value)
	}
}
