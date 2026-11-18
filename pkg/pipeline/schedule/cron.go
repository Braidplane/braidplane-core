package schedule

import (
	"errors"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// ParseCron accepts standard cron schedules, including descriptors and timezone prefixes.
func ParseCron(spec string) (cron.Schedule, error) {
	// The cron parser expects a space after an inline timezone.
	if (strings.HasPrefix(spec, "TZ=") || strings.HasPrefix(spec, "CRON_TZ=")) && !strings.Contains(spec, " ") {
		return nil, errors.New("schedule expression required after timezone")
	}
	return cron.ParseStandard(spec)
}

// HasCalendarMatch probes a fixed window containing a leap day so cron's
// bounded search does not reject a valid schedule during a long leap-year gap.
func HasCalendarMatch(schedule cron.Schedule, location *time.Location) bool {
	anchor := time.Date(2024, time.January, 1, 0, 0, 0, 0, location)
	return schedule.Next(anchor).After(anchor)
}
