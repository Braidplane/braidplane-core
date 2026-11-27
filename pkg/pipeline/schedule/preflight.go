package schedule

import (
	"time"

	"github.com/robfig/cron/v3"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const MaxPreflightLeadTime = 24 * time.Hour

var ErrInvalidPreflightLeadTime = xerror.InvalidInput(xerror.PipelineTrigger).
	Detail("preflight lead time must be a whole-minute duration from 1m through 24h")

func ParsePreflightLeadTime(value string) (time.Duration, error) {
	duration, err := time.ParseDuration(value)
	if err != nil || !ValidPreflightLeadTime(duration) {
		return 0, ErrInvalidPreflightLeadTime
	}
	return duration, nil
}

func ValidPreflightLeadTime(duration time.Duration) bool {
	return duration >= time.Minute && duration <= MaxPreflightLeadTime && duration%time.Minute == 0
}

// ParsePreflightCron requires occurrences that are independent of the dispatch cursor.
func ParsePreflightCron(spec string) (cron.Schedule, error) {
	schedule, err := ParseCron(spec)
	if err != nil {
		return nil, err
	}
	if _, relative := schedule.(cron.ConstantDelaySchedule); relative {
		return nil, xerror.InvalidInput(xerror.PipelineTrigger).Detail("preflight requires a calendar cron expression")
	}
	return schedule, nil
}
