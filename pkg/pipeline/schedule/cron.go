package schedule

import (
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// ParseCron accepts standard cron schedules, including descriptors and timezone prefixes.
func ParseCron(spec string) (cron.Schedule, error) {
	// The cron parser expects a space after an inline timezone.
	if (strings.HasPrefix(spec, "TZ=") || strings.HasPrefix(spec, "CRON_TZ=")) && !strings.Contains(spec, " ") {
		return nil, xerror.ValidationFailed().Detail("schedule expression required after timezone")
	}
	schedule, err := cron.ParseStandard(spec)
	if err != nil {
		return nil, xerror.ValidationFailed().With(err)
	}
	return schedule, nil
}

// HasCalendarMatch probes a fixed window containing a leap day so cron's
// bounded search does not reject a valid schedule during a long leap-year gap.
func HasCalendarMatch(schedule cron.Schedule, location *time.Location) bool {
	anchor := time.Date(2024, time.January, 1, 0, 0, 0, 0, location)
	return schedule.Next(anchor).After(anchor)
}
