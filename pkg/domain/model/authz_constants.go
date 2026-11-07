//go:generate -command enumer go tool -modfile=../../../tools/go.mod enumer
//go:generate enumer -type=Resource -transform=snake -output=authz_constants_resource_string.go -text -linecomment
//go:generate enumer -type=Action -transform=snake -output=authz_constants_action_string.go -text -linecomment
package model

// Resource represents an authorization resource type.
type Resource int

const (
	ResourceUnknown Resource = iota // unknown

	// Global resources (system-level)
	ResourceOrganizations // organizations
	ResourceProfile       // profile
	ResourceUsers         // users

	// Organization-scoped resources
	ResourceMembers           // members
	ResourceRoles             // roles
	ResourceAPIKeys           // api_keys
	ResourceProjects          // projects
	ResourceEnvironments      // environments
	ResourceDataSources       // datasources
	ResourceQueries           // queries
	ResourcePipelines         // pipelines
	ResourcePipelineTriggers  // pipeline_triggers
	ResourceEvents            // events
	ResourceNotifications     // notifications
	ResourceAlertPolicies     // alert_policies
	ResourceAlertDestinations // alert_destinations
)

// Action represents an authorization action type.
type Action int

const (
	ActionUnknown Action = iota // unknown
	ActionCreate                // create
	ActionDelete                // delete
	ActionRestore               // restore
	ActionPurge                 // purge
	ActionRead                  // read
	ActionUpdate                // update
	ActionExecute               // execute
)

const (
	Unknown Action = ActionUnknown
	Create  Action = ActionCreate
	Delete  Action = ActionDelete
	Restore Action = ActionRestore
	Purge   Action = ActionPurge
	Read    Action = ActionRead
	Update  Action = ActionUpdate
	Execute Action = ActionExecute
)
