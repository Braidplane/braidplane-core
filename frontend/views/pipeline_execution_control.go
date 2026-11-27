package views

import "fmt"

func PipelineExecutionControlHref(org, project, environment, pipeline string) string {
	return fmt.Sprintf("/orgs/%s/projects/%s/pipelines/%s/environments/%s/execution-control", org, project, pipeline, environment)
}

func executionAttemptPhase(phase string, paused bool) string {
	if phase == "queued" && paused {
		return "Held"
	}
	switch phase {
	case "queued":
		return "Queued"
	case "active":
		return "Active"
	default:
		return "Finished during pause"
	}
}
