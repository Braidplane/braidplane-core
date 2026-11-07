//go:generate -command enumer go tool -modfile=../../../tools/go.mod enumer
//go:generate enumer -type=EventActorType -transform=snake -output=event_actor_type_string.go -text -json -linecomment
//go:generate enumer -type=EventClass -transform=snake -output=event_class_string.go -text -json -linecomment
//go:generate enumer -type=EventEntityType -transform=snake -output=event_entity_type_string.go -text -json -linecomment
//go:generate enumer -type=EventAction -transform=snake -output=event_action_string.go -text -json -linecomment
package model

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type EventActorType int

const (
	EventActorTypeUnknown  EventActorType = iota // unknown
	EventActorTypeUser                           // user
	EventActorTypeAPIKey                         // api_key
	EventActorTypeSystem                         // system
	EventActorTypeExternal                       // external
)

type EventClass int

const (
	EventClassUnknown       EventClass = iota // unknown
	EventClassActivity                        // activity
	EventClassSecurityAudit                   // security_audit
)

const CurrentEventSchemaVersion = 1

type EventActor struct {
	Type EventActorType
	Slug *slug.Slug
}

func UserEventActor(actorSlug slug.Slug) EventActor {
	return EventActor{
		Type: EventActorTypeUser,
		Slug: &actorSlug,
	}
}

func APIKeyEventActor(apiKeySlug slug.Slug) EventActor {
	return EventActor{
		Type: EventActorTypeAPIKey,
		Slug: &apiKeySlug,
	}
}

func SystemEventActor() EventActor {
	return EventActor{Type: EventActorTypeSystem}
}

func ExternalEventActor() EventActor {
	return EventActor{Type: EventActorTypeExternal}
}

func (a EventActor) UserSlugValue() slug.Slug {
	if a.Type != EventActorTypeUser || a.Slug == nil {
		return ""
	}
	return *a.Slug
}

type EventEntityType int

const (
	EventEntityTypeUnknown            EventEntityType = iota // unknown
	EventEntityTypeOrganization                              // organization
	EventEntityTypeMember                                    // member
	EventEntityTypeMemberInvitation                          // member_invitation
	EventEntityTypeRole                                      // role
	EventEntityTypeAPIKey                                    // api_key
	EventEntityTypeDataSource                                // data_source
	EventEntityTypeDataSourceRef                             // data_source_ref
	EventEntityTypeDataSourceBinding                         // data_source_binding
	EventEntityTypeProject                                   // project
	EventEntityTypeEnvironment                               // environment
	EventEntityTypeProjectEnvironment                        // project_environment
	EventEntityTypeQuery                                     // query
	EventEntityTypeQueryExecution                            // query_execution
	EventEntityTypePipeline                                  // pipeline
	EventEntityTypePipelineDeployment                        // pipeline_deployment
	EventEntityTypePipelineVersion                           // pipeline_version
	EventEntityTypePipelineRun                               // pipeline_run
	EventEntityTypePipelineCheckpoint                        // pipeline_checkpoint
	EventEntityTypePipelineTrigger                           // pipeline_trigger
	EventEntityTypeNotification                              // notification
	EventEntityTypeAlertDestination                          // alert_destination
	EventEntityTypeAlertPolicy                               // alert_policy
)

type EventAction int

const (
	EventActionUnknown        EventAction = iota // unknown
	EventActionCreate                            // create
	EventActionUpdate                            // update
	EventActionDelete                            // delete
	EventActionRestore                           // restore
	EventActionPurge                             // purge
	EventActionExecute                           // execute
	EventActionRefresh                           // refresh
	EventActionTestConnection                    // test_connection
	EventActionRotate                            // rotate
	EventActionRevoke                            // revoke
	EventActionReconcile                         // reconcile
	EventActionDeploy                            // deploy
	EventActionPublish                           // publish
	EventActionEnable                            // enable
	EventActionDisable                           // disable
	EventActionAccept                            // accept
	EventActionDecline                           // decline
	EventActionExpire                            // expire
	EventActionAutoAccept                        // auto_accept
	EventActionAcknowledge                       // acknowledge
	EventActionResolve                           // resolve
	EventActionRead                              // read
	EventActionValidate                          // validate
)

type Event struct {
	Slug          slug.Slug
	EventClass    EventClass
	ActorType     EventActorType
	ActorSlug     *slug.Slug
	EntityType    EventEntityType
	EntitySlug    *slug.Slug
	Action        EventAction
	OccurredAt    time.Time
	RequestID     *string
	SchemaVersion int
	Metadata      map[string]any
	CreatedAt     time.Time
}

type EventSourceMetadata struct {
	SourceKind    EventEntityType
	SourceActions []EventAction
}

type EventSourceRefOption struct {
	Value slug.Slug
	Label string
}

type EventSourceMetadataResponse struct {
	NotificationTypes []NotificationType
	Sources           []EventSourceMetadata
	SourceRefs        []EventSourceRefOption
}

var alertPolicyEventSourceMetadata = []EventSourceMetadata{
	{SourceKind: EventEntityTypeOrganization, SourceActions: []EventAction{EventActionCreate, EventActionUpdate}},
	{SourceKind: EventEntityTypeMember, SourceActions: []EventAction{EventActionCreate, EventActionUpdate, EventActionDelete}},
	{SourceKind: EventEntityTypeMemberInvitation, SourceActions: []EventAction{EventActionCreate, EventActionRefresh, EventActionRevoke, EventActionAccept, EventActionDecline, EventActionExpire, EventActionAutoAccept}},
	{SourceKind: EventEntityTypeRole, SourceActions: []EventAction{EventActionCreate, EventActionUpdate, EventActionDelete, EventActionRestore}},
	{SourceKind: EventEntityTypeAPIKey, SourceActions: []EventAction{EventActionCreate, EventActionUpdate, EventActionRevoke, EventActionDelete, EventActionRotate}},
	{SourceKind: EventEntityTypeDataSource, SourceActions: []EventAction{EventActionCreate, EventActionUpdate, EventActionRotate, EventActionRevoke, EventActionDelete, EventActionRestore, EventActionRefresh}},
	{SourceKind: EventEntityTypeDataSourceRef, SourceActions: []EventAction{EventActionCreate, EventActionUpdate, EventActionDelete}},
	{SourceKind: EventEntityTypeDataSourceBinding, SourceActions: []EventAction{EventActionUpdate, EventActionDelete}},
	{SourceKind: EventEntityTypeProject, SourceActions: []EventAction{EventActionCreate, EventActionUpdate, EventActionDelete}},
	{SourceKind: EventEntityTypeEnvironment, SourceActions: []EventAction{EventActionCreate, EventActionUpdate, EventActionDelete}},
	{SourceKind: EventEntityTypeProjectEnvironment, SourceActions: []EventAction{EventActionEnable, EventActionDisable, EventActionUpdate}},
	{SourceKind: EventEntityTypeQuery, SourceActions: []EventAction{EventActionCreate, EventActionUpdate, EventActionDelete, EventActionRestore, EventActionPurge}},
	{SourceKind: EventEntityTypeQueryExecution, SourceActions: []EventAction{EventActionExecute}},
	{SourceKind: EventEntityTypePipeline, SourceActions: []EventAction{EventActionCreate, EventActionUpdate, EventActionDelete, EventActionRestore, EventActionPurge, EventActionReconcile, EventActionValidate, EventActionExecute}},
	{SourceKind: EventEntityTypePipelineDeployment, SourceActions: []EventAction{EventActionDeploy}},
	{SourceKind: EventEntityTypePipelineVersion, SourceActions: []EventAction{EventActionPublish}},
	{SourceKind: EventEntityTypePipelineRun, SourceActions: []EventAction{EventActionExecute}},
	{SourceKind: EventEntityTypePipelineTrigger, SourceActions: []EventAction{EventActionCreate, EventActionUpdate, EventActionDelete, EventActionExecute}},
	{SourceKind: EventEntityTypeNotification, SourceActions: []EventAction{EventActionAcknowledge, EventActionResolve}},
}

// AlertPolicyEventSourceMetadata returns the entity/action pairs alert policies can target.
func AlertPolicyEventSourceMetadata() []EventSourceMetadata {
	ret := make([]EventSourceMetadata, len(alertPolicyEventSourceMetadata))
	for i, source := range alertPolicyEventSourceMetadata {
		actions := make([]EventAction, len(source.SourceActions))
		copy(actions, source.SourceActions)
		ret[i] = EventSourceMetadata{
			SourceKind:    source.SourceKind,
			SourceActions: actions,
		}
	}
	return ret
}

// AlertPolicySupportsEventSource reports whether a source entity/action pair is exposed to alert policies.
func AlertPolicySupportsEventSource(sourceKind EventEntityType, sourceAction *EventAction) bool {
	if sourceKind == EventEntityTypeUnknown {
		return false
	}
	for _, source := range alertPolicyEventSourceMetadata {
		if source.SourceKind != sourceKind {
			continue
		}
		if sourceAction == nil {
			return true
		}
		for _, action := range source.SourceActions {
			if action == *sourceAction {
				return true
			}
		}
		return false
	}
	return false
}
