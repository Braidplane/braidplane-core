package scheduler

import (
	"github.com/riverqueue/river"
	"github.com/robfig/cron/v3"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/retentionsvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

const (
	pipelineScheduleDispatchCronSchedule = "* * * * *"
)

func PeriodicJobs() ([]*river.PeriodicJob, error) {
	eventPruneJob, err := newEventPrunePeriodicJob()
	if err != nil {
		return nil, xerror.Wrapf("build event prune periodic job: %w", err)
	}
	pipelineScheduleJob, err := newPipelineScheduleDispatchPeriodicJob()
	if err != nil {
		return nil, xerror.Wrapf("build pipeline schedule periodic job: %w", err)
	}

	return []*river.PeriodicJob{eventPruneJob, pipelineScheduleJob}, nil
}

func newEventPrunePeriodicJob() (*river.PeriodicJob, error) {
	schedule, err := eventPruneSchedule()
	if err != nil {
		return nil, xerror.Wrapf("parse event prune cron schedule: %w", err)
	}

	return river.NewPeriodicJob(
		schedule,
		func() (river.JobArgs, *river.InsertOpts) {
			args := jobs.EventPruneArgs{}
			opts := args.InsertOpts()
			return args, &opts
		},
		&river.PeriodicJobOpts{ID: jobs.EventPruneArgs{}.Kind()},
	), nil
}

func eventPruneSchedule() (cron.Schedule, error) {
	schedule, err := cron.ParseStandard(retentionsvc.DailyCronSchedule)
	if err != nil {
		return nil, xerror.Wrapf("parse UTC retention maintenance schedule: %w", err)
	}
	return schedule, nil
}

func newPipelineScheduleDispatchPeriodicJob() (*river.PeriodicJob, error) {
	schedule, err := cron.ParseStandard(pipelineScheduleDispatchCronSchedule)
	if err != nil {
		return nil, xerror.Wrapf("parse pipeline schedule dispatch cron schedule: %w", err)
	}

	return river.NewPeriodicJob(
		schedule,
		func() (river.JobArgs, *river.InsertOpts) {
			args := jobs.PipelineScheduleDispatchArgs{}
			opts := args.InsertOpts()
			return args, &opts
		},
		&river.PeriodicJobOpts{ID: jobs.PipelineScheduleDispatchArgs{}.Kind()},
	), nil
}
