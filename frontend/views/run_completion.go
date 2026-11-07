package views

import (
	"net/url"
	"strconv"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

type RunCompletionInspection struct {
	Connected                  *api.PipelineRunConnected
	CurrentSubscriptions       []api.PipelineTrigger
	CurrentSubscriptionsLoaded bool
	Evidence                   *api.PipelineRunCompletion
	ErrorSummary               string
}

func completionRunHref(org, project, environment, run string) string {
	u, _ := url.Parse(buildEnvironmentRunDetailHref(org, project, environment, run))
	q := u.Query()
	q.Set("section", "completion")
	u.RawQuery = q.Encode()
	return u.String()
}

func completionRootState(evidence api.PipelineRunCompletion) string {
	if evidence.Closed {
		if evidence.Outcome != nil {
			return "Closed · " + *evidence.Outcome
		}
		return "Closed"
	}
	if evidence.WaitingForRetry {
		return "waiting_for_retry"
	}
	return "Not closed"
}

func completionText(value *string) string {
	if value == nil {
		return "Not recorded"
	}
	return *value
}

func completionRevision(value *int64) string {
	if value == nil {
		return "Not recorded"
	}
	return strconv.FormatInt(*value, 10)
}

func completionCondition(value *api.PipelineCompletionCondition) string {
	if value == nil {
		return "Condition not retained"
	}
	return string(*value)
}

func completionPending(evidence *api.PipelineRunCompletion) bool {
	if evidence == nil {
		return false
	}
	if !evidence.Closed {
		if evidence.WaitingForRetry {
			return true
		}
		for _, attempt := range evidence.Attempts {
			if attempt.Status == "queued" || attempt.Status == "running" {
				return true
			}
		}
		return false
	}
	for _, decision := range evidence.Children {
		if decision.State == "pending" || (decision.ChildRunSlug != nil && decision.ChildOutcome == nil) {
			return true
		}
	}
	return false
}

func completionSubscriptionState(trigger api.PipelineTrigger, evidence api.PipelineRunCompletion) string {
	if !trigger.Enabled {
		return "Disabled"
	}
	if trigger.Completion == nil || !trigger.Completion.ExecutionGrantEnabled {
		return "Execution grant revoked"
	}
	if evidence.Closed && evidence.Outcome != nil {
		expected := "succeeded"
		if trigger.Completion.Condition == api.PipelineCompletionConditionFinalFailure {
			expected = "failed"
		}
		if *evidence.Outcome != expected {
			return "Current condition does not match recorded closure"
		}
	}
	return "Waiting subscription for the next matching logical closure"
}
