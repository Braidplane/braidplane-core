package views

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/a-h/templ"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

// AlertDestinationFormData stores sticky values for alert destination forms.
type AlertDestinationFormData struct {
	ErrorMessage   string
	InvalidField   string
	Name           string
	Slug           string
	Type           string
	To             string
	ConnectionSlug string
	Enabled        bool
}

// AlertPolicyFormData stores sticky values for alert policy forms.
type AlertPolicyFormData struct {
	NotificationReturn string
	PolicyKind         string
	Name               string
	Slug               string
	Scope              string
	ProjectSlug        string
	EnvironmentSlug    string
	NotificationType   string
	Severity           string
	LifecycleState     string
	SourceKind         string
	SourceAction       string
	SourceRef          string
	DestinationSlugs   []string
	Enabled            bool
}

// AlertEditFormOverrides carries sticky submitted values for failed edit forms.
type AlertEditFormOverrides struct {
	NotificationReturn  string
	DestinationSlug     string
	DestinationFormData AlertDestinationFormData
	PolicySlug          string
	PolicyFormData      AlertPolicyFormData
}

// DestinationFormDataFor returns submitted edit values for the matching destination.
func (overrides AlertEditFormOverrides) DestinationFormDataFor(destination api.AlertDestination) AlertDestinationFormData {
	if strings.TrimSpace(overrides.DestinationSlug) == destination.Slug {
		return overrides.DestinationFormData
	}
	return AlertDestinationFormDataFromDestination(destination)
}

// PolicyFormDataFor returns submitted edit values for the matching policy.
func (overrides AlertEditFormOverrides) PolicyFormDataFor(policy api.AlertPolicy) AlertPolicyFormData {
	if strings.TrimSpace(overrides.PolicySlug) == policy.Slug {
		return overrides.PolicyFormData
	}
	return AlertPolicyFormDataFromPolicy(policy)
}

type AlertTab string

const (
	AlertTabDestinations AlertTab = "destinations"
	AlertTabPolicies     AlertTab = "policies"
)

type AlertPolicyKind string

const (
	AlertPolicyKindNotificationType AlertPolicyKind = "notification_type"
	AlertPolicyKindSeverity         AlertPolicyKind = "severity"
	AlertPolicyKindSource           AlertPolicyKind = "source"
)

type alertFormOption struct {
	Value string
	Label string
	Help  string
}

type AlertFormOption = alertFormOption

type AlertPolicySelectorOptions struct {
	NotificationTypes  []alertFormOption
	Severities         []alertFormOption
	LifecycleStates    []alertFormOption
	Projects           []alertFormOption
	Environments       []alertFormOption
	SourceKinds        []alertFormOption
	SourceActions      []alertFormOption
	SourceRefs         []alertFormOption
	ProjectEnabled     bool
	EnvironmentEnabled bool
	SourceRefEnabled   bool
}

// AlertPolicyDynamicOptions carries API-backed selector choices for the HTMX policy partial.
type AlertPolicyDynamicOptions struct {
	Projects       []AlertFormOption
	Environments   []AlertFormOption
	SourceMetadata []api.AlertPolicySourceMetadata
	SourceRefs     []AlertFormOption
}

// DefaultAlertDestinationFormData returns alert destination form defaults.
func DefaultAlertDestinationFormData() AlertDestinationFormData {
	return AlertDestinationFormData{
		Type:    string(api.AlertDestinationTypeEmail),
		Enabled: true,
	}
}

// DefaultAlertPolicyFormData returns alert policy form defaults.
func DefaultAlertPolicyFormData() AlertPolicyFormData {
	return AlertPolicyFormData{
		PolicyKind:       string(AlertPolicyKindNotificationType),
		Scope:            string(api.AlertPolicyScopeOrganization),
		NotificationType: string(api.NotificationTypePipelineRetryExhausted),
		Enabled:          true,
	}
}

// AlertDestinationFormDataFromDestination maps an API destination to edit form values.
func AlertDestinationFormDataFromDestination(destination api.AlertDestination) AlertDestinationFormData {
	return AlertDestinationFormData{
		Name:           strings.TrimSpace(destination.Name),
		Slug:           destination.Slug,
		Type:           string(destination.Type),
		To:             strings.Join(AlertDestinationRecipientValues(destination), ", "),
		ConnectionSlug: AlertDestinationConfigValue(destination, "connection_slug"),
		Enabled:        destination.Enabled,
	}
}

// AlertPolicyFormDataFromPolicy maps an API policy to edit form values.
func AlertPolicyFormDataFromPolicy(policy api.AlertPolicy) AlertPolicyFormData {
	form := AlertPolicyFormData{
		Name:             strings.TrimSpace(policy.Name),
		Slug:             policy.Slug,
		Scope:            string(policy.Scope),
		ProjectSlug:      alertNullableSlugString(policy.ProjectSlug),
		EnvironmentSlug:  alertNullableSlugString(policy.EnvironmentSlug),
		NotificationType: alertNullableNotificationTypeString(policy.NotificationType),
		Severity:         alertNullableNotificationSeverityString(policy.Severity),
		LifecycleState:   alertNullableNotificationLifecycleStateString(policy.LifecycleState),
		SourceKind:       alertNullableAlertPolicySourceKindString(policy.SourceKind),
		SourceAction:     alertNullableAlertPolicySourceActionString(policy.SourceAction),
		SourceRef:        alertNullableString(policy.SourceRef),
		DestinationSlugs: alertSlugStrings(policy.DestinationSlugs),
		Enabled:          policy.Enabled,
	}
	form.PolicyKind = string(AlertPolicyKindForForm(form))
	return AlertPolicyFormDataForRender(form)
}

// AlertDestinationCountLabel formats destination counts for page badges.
func AlertDestinationCountLabel(count int) string {
	if count == 1 {
		return "1 destination"
	}
	return fmt.Sprintf("%d destinations", count)
}

// AlertPolicyCountLabel formats policy counts for page badges.
func AlertPolicyCountLabel(count int) string {
	if count == 1 {
		return "1 policy"
	}
	return fmt.Sprintf("%d policies", count)
}

// AlertTabFromString normalizes alert tab query values to a supported section.
func AlertTabFromString(value string) AlertTab {
	switch AlertTab(strings.TrimSpace(value)) {
	case AlertTabPolicies:
		return AlertTabPolicies
	default:
		return AlertTabDestinations
	}
}

// AlertTabHref returns the organization alerts URL for a route-backed tab.
func AlertTabHref(orgSlug string, tab AlertTab) string {
	values := url.Values{}
	values.Set("tab", string(AlertTabFromString(string(tab))))
	return "/orgs/" + strings.TrimSpace(orgSlug) + "/alerts?" + values.Encode()
}

func AlertPolicySelectorOptionsHref(orgSlug string) string {
	return "/orgs/" + strings.TrimSpace(orgSlug) + "/alerts/policies/selector-options"
}

func AlertPolicyCreateSelectorID() string {
	return "alert-policy-create-selectors"
}

func AlertPolicyEditSelectorID(policy api.AlertPolicy) string {
	return "alert-policy-" + strings.TrimSpace(policy.Slug) + "-selectors"
}

func AlertPolicyFormSelectorID(form AlertPolicyFormData, includeEnabled bool) string {
	if includeEnabled {
		if policySlug := strings.TrimSpace(form.Slug); policySlug != "" {
			return "alert-policy-" + policySlug + "-selectors"
		}
	}
	return AlertPolicyCreateSelectorID()
}

// AlertTabClass returns the DaisyUI tab class for the selected alerts section.
func AlertTabClass(activeTab, tab AlertTab) string {
	if AlertTabFromString(string(activeTab)) == tab {
		return tabActiveClass
	}
	return tabClass
}

// AlertTabID returns the DOM ID used to associate an alerts tab with its panel.
func AlertTabID(tab AlertTab) string {
	switch tab {
	case AlertTabPolicies:
		return "alerts-policies-tab-control"
	default:
		return "alerts-destinations-tab-control"
	}
}

// AlertTabPanelID returns the DOM ID for the tabpanel associated with an alerts tab.
func AlertTabPanelID(tab AlertTab) string {
	switch tab {
	case AlertTabPolicies:
		return "alerts-policies-tab"
	default:
		return "alerts-destinations-tab"
	}
}

// AlertTabAriaSelected returns the WAI-ARIA selected state for an alerts tab.
func AlertTabAriaSelected(activeTab, tab AlertTab) string {
	if AlertTabFromString(string(activeTab)) == tab {
		return ariaStateTrue
	}
	return ariaStateFalse
}

// AlertDestinationTypeOptions returns supported alert destination adapters.
func AlertDestinationTypeOptions() []alertFormOption {
	return []alertFormOption{
		{Value: string(api.AlertDestinationTypeEmail), Label: "Email"},
		{Value: string(api.AlertDestinationTypeSlack), Label: "Slack"},
	}
}

// AlertPolicyScopeOptions returns supported alert policy scopes.
func AlertPolicyScopeOptions() []alertFormOption {
	return []alertFormOption{
		{Value: string(api.AlertPolicyScopeOrganization), Label: "Organization"},
		{Value: string(api.AlertPolicyScopeProject), Label: "Project"},
		{Value: string(api.AlertPolicyScopeProjectEnvironment), Label: "Project environment"},
	}
}

// AlertPolicyKindOptions returns the primary routing modes exposed by the policy form.
func AlertPolicyKindOptions() []alertFormOption {
	return []alertFormOption{
		{
			Value: string(AlertPolicyKindNotificationType),
			Label: "Notification type",
			Help:  "Route one notification family within scope and lifecycle filters.",
		},
		{
			Value: string(AlertPolicyKindSeverity),
			Label: "Severity",
			Help:  "Route every matching severity, optionally narrowed by source.",
		},
		{
			Value: string(AlertPolicyKindSource),
			Label: "Source",
			Help:  "Route notifications from a specific event entity and action.",
		},
	}
}

// AlertPolicyFormDataForRender normalizes sticky form state for the selected policy kind.
func AlertPolicyFormDataForRender(form AlertPolicyFormData) AlertPolicyFormData {
	form.PolicyKind = string(AlertPolicyKindForForm(form))
	if strings.TrimSpace(form.Scope) == "" {
		form.Scope = string(api.AlertPolicyScopeOrganization)
	}
	switch {
	case !AlertPolicyProjectScopeEnabled(form):
		form.ProjectSlug = ""
		form.EnvironmentSlug = ""
	case !AlertPolicyEnvironmentScopeEnabled(form):
		form.EnvironmentSlug = ""
	case strings.TrimSpace(form.ProjectSlug) == "":
		form.EnvironmentSlug = ""
	}

	kind := AlertPolicyKindForForm(form)
	form.PolicyKind = string(kind)
	switch kind {
	case AlertPolicyKindNotificationType:
		if strings.TrimSpace(form.NotificationType) == "" {
			form.NotificationType = string(api.NotificationTypePipelineRetryExhausted)
		}
	case AlertPolicyKindSeverity:
		if strings.TrimSpace(form.Severity) == "" {
			form.Severity = string(api.NotificationSeverityCritical)
		}
		form.NotificationType = ""
	case AlertPolicyKindSource:
		form.NotificationType = ""
		form.Severity = ""
		if strings.TrimSpace(form.SourceKind) == "" {
			form.SourceKind = string(api.EventEntityTypePipelineRun)
		}
	}

	if strings.TrimSpace(form.SourceKind) == "" {
		form.SourceAction = ""
	}
	if !AlertPolicySourceRefEnabled(form) {
		form.SourceRef = ""
	}
	return form
}

// AlertPolicyKindForForm derives the selected UI routing mode from sticky or persisted fields.
func AlertPolicyKindForForm(form AlertPolicyFormData) AlertPolicyKind {
	kind := AlertPolicyKind(strings.TrimSpace(form.PolicyKind))
	switch kind {
	case AlertPolicyKindNotificationType, AlertPolicyKindSeverity, AlertPolicyKindSource:
		return kind
	}

	notificationType := api.NotificationType(strings.TrimSpace(form.NotificationType))
	if notificationType.Valid() && notificationType != api.NotificationTypeUnknown {
		return AlertPolicyKindNotificationType
	}
	if strings.TrimSpace(form.Severity) != "" {
		return AlertPolicyKindSeverity
	}
	if strings.TrimSpace(form.SourceKind) != "" || strings.TrimSpace(form.SourceRef) != "" || strings.TrimSpace(form.SourceAction) != "" {
		return AlertPolicyKindSource
	}
	return AlertPolicyKindNotificationType
}

// AlertPolicyKindCardClass returns the card-like radio label class for one policy kind.
func AlertPolicyKindCardClass(form AlertPolicyFormData, value string) string {
	base := "flex h-full cursor-pointer gap-3 rounded-lg border p-3 transition-colors"
	if string(AlertPolicyKindForForm(form)) == strings.TrimSpace(value) {
		return base + " border-primary bg-primary/10 text-base-content"
	}
	return base + " border-base-300 bg-base-200/35 text-base-content hover:border-primary/50 hover:bg-base-200"
}

// AlertPolicyFormAttrs returns optional form attributes for dirty-tracked policy edits.
func AlertPolicyFormAttrs(dirtyFormID string, disableSubmit bool) templ.Attributes {
	if !AlertPolicyUsesDirtySubmit(dirtyFormID, disableSubmit) {
		return templ.Attributes{}
	}
	return templ.Attributes{"data-dirty-form": strings.TrimSpace(dirtyFormID)}
}

// AlertPolicyUsesDirtySubmit reports whether a policy form should wait for dirty-state changes.
func AlertPolicyUsesDirtySubmit(dirtyFormID string, disableSubmit bool) bool {
	return strings.TrimSpace(dirtyFormID) != "" && !disableSubmit
}

// AlertPolicySelectorLoadAttrs returns HTMX attributes for initial dynamic selector loading.
func AlertPolicySelectorLoadAttrs(
	orgSlug string,
	selectorTargetID string,
	form AlertPolicyFormData,
	options AlertPolicyDynamicOptions,
) templ.Attributes {
	if !AlertPolicyShouldLoadSelectorOptions(form, options) {
		return templ.Attributes{}
	}
	return templ.Attributes{
		"hx-get":        AlertPolicySelectorOptionsHref(orgSlug),
		"hx-target":     "#" + selectorTargetID,
		"hx-select":     "#" + selectorTargetID,
		"hx-include":    "closest form",
		"hx-trigger":    "load",
		"hx-swap":       "outerHTML",
		"hx-disinherit": "hx-select",
	}
}

// AlertPolicyShouldLoadSelectorOptions reports whether the SSR form still needs API-backed selector options.
func AlertPolicyShouldLoadSelectorOptions(form AlertPolicyFormData, options AlertPolicyDynamicOptions) bool {
	form = AlertPolicyFormDataForRender(form)
	if AlertPolicyProjectScopeEnabled(form) && options.Projects == nil {
		return true
	}
	if AlertPolicyEnvironmentScopeEnabled(form) && strings.TrimSpace(form.ProjectSlug) != "" && options.Environments == nil {
		return true
	}
	if options.SourceMetadata == nil {
		return true
	}
	return AlertPolicySourceRefEnabled(form) && options.SourceRefs == nil
}

// AlertPolicyProjectScopeEnabled reports whether the project selector should submit.
func AlertPolicyProjectScopeEnabled(form AlertPolicyFormData) bool {
	switch api.AlertPolicyScope(strings.TrimSpace(form.Scope)) {
	case api.AlertPolicyScopeProject, api.AlertPolicyScopeProjectEnvironment:
		return true
	default:
		return false
	}
}

// AlertPolicyEnvironmentScopeEnabled reports whether the environment selector should submit.
func AlertPolicyEnvironmentScopeEnabled(form AlertPolicyFormData) bool {
	return api.AlertPolicyScope(strings.TrimSpace(form.Scope)) == api.AlertPolicyScopeProjectEnvironment
}

// AlertPolicyProjectSelectDisabled reports whether the project dropdown should submit.
func AlertPolicyProjectSelectDisabled(form AlertPolicyFormData) bool {
	return !AlertPolicyProjectScopeEnabled(form)
}

// AlertPolicyEnvironmentSelectDisabled reports whether the environment dropdown should submit.
func AlertPolicyEnvironmentSelectDisabled(form AlertPolicyFormData) bool {
	return !AlertPolicyEnvironmentScopeEnabled(form) || strings.TrimSpace(form.ProjectSlug) == ""
}

// AlertPolicyScopeFieldClass hides inactive scope fields while keeping layout predictable.
func AlertPolicyScopeFieldClass(enabled bool) string {
	if enabled {
		return "form-control w-full"
	}
	return "form-control hidden w-full"
}

// AlertNotificationTypeOptions returns notification type options available to alert policies.
func AlertNotificationTypeOptions() []alertFormOption {
	ret := make([]alertFormOption, 0, len(notificationTypeOptions(false))+1)
	ret = append(ret, alertFormOption{Value: "", Label: "Any notification type"})
	for _, option := range notificationTypeOptions(false) {
		ret = append(ret, alertFormOption{Value: option.Value, Label: option.Label})
	}
	return ret
}

// AlertSeverityOptions returns severity options available to alert policies.
func AlertSeverityOptions() []alertFormOption {
	ret := make([]alertFormOption, 0, len(notificationSeverityOptions())+1)
	ret = append(ret, alertFormOption{Value: "", Label: "Any severity"})
	for _, option := range notificationSeverityOptions() {
		ret = append(ret, alertFormOption{Value: option.Value, Label: option.Label})
	}
	return ret
}

// AlertLifecycleOptions returns lifecycle state options available to alert policies.
func AlertLifecycleOptions() []alertFormOption {
	return []alertFormOption{
		{Value: "", Label: "Any lifecycle"},
		{Value: string(api.NotificationLifecycleStateOpen), Label: "Open"},
		{Value: string(api.NotificationLifecycleStateAcknowledged), Label: "Acknowledged"},
		{Value: string(api.NotificationLifecycleStateResolved), Label: "Resolved"},
	}
}

// AlertSourceKindOptions returns API-backed source selectors available to alert policies.
func AlertSourceKindOptions(form AlertPolicyFormData, sources []api.AlertPolicySourceMetadata) []alertFormOption {
	options := make([]alertFormOption, 0, len(sources))
	for _, source := range sources {
		value := strings.TrimSpace(string(source.SourceKind))
		if value == "" || source.SourceKind == api.EventEntityTypeUnknown {
			continue
		}
		options = append(options, alertFormOption{Value: value, Label: alertPolicyEnumLabel(value)})
	}
	sortAlertFormOptions(options)
	if value := strings.TrimSpace(form.SourceKind); value != "" && !alertFormOptionsContain(options, value) {
		options = append(options, alertFormOption{Value: value, Label: alertPolicyEnumLabel(value)})
	}
	return append([]alertFormOption{{Value: "", Label: "Any source"}}, options...)
}

// AlertSourceActionOptions returns source actions available for the current source selector.
func AlertSourceActionOptions(form AlertPolicyFormData, sources []api.AlertPolicySourceMetadata) []alertFormOption {
	kind := strings.TrimSpace(form.SourceKind)
	options := []alertFormOption{}
	if kind != "" {
		for _, source := range sources {
			if strings.TrimSpace(string(source.SourceKind)) != kind {
				continue
			}
			for _, action := range source.SourceActions {
				value := strings.TrimSpace(string(action))
				if value == "" || action == api.EventActionUnknown {
					continue
				}
				options = append(options, alertFormOption{Value: value, Label: alertPolicyEnumLabel(value)})
			}
			break
		}
	}
	sortAlertFormOptions(options)
	if value := strings.TrimSpace(form.SourceAction); value != "" && !alertFormOptionsContain(options, value) {
		options = append(options, alertFormOption{Value: value, Label: alertPolicyEnumLabel(value)})
	}
	return append([]alertFormOption{{Value: "", Label: "Any action"}}, options...)
}

// AlertPolicySelectorOptionsFor returns form selector options filtered by the current policy scope.
func AlertPolicySelectorOptionsFor(
	form AlertPolicyFormData,
	dynamicOptions AlertPolicyDynamicOptions,
) AlertPolicySelectorOptions {
	form = AlertPolicyFormDataForRender(form)
	return AlertPolicySelectorOptions{
		NotificationTypes:  AlertNotificationTypeOptions(),
		Severities:         AlertSeverityOptions(),
		LifecycleStates:    AlertLifecycleOptions(),
		Projects:           alertPolicyProjectOptions(form, dynamicOptions.Projects),
		Environments:       alertPolicyEnvironmentOptions(form, dynamicOptions.Environments),
		SourceKinds:        AlertSourceKindOptions(form, dynamicOptions.SourceMetadata),
		SourceActions:      AlertSourceActionOptions(form, dynamicOptions.SourceMetadata),
		SourceRefs:         alertPolicySourceRefOptions(form, dynamicOptions.SourceRefs),
		ProjectEnabled:     AlertPolicyProjectScopeEnabled(form),
		EnvironmentEnabled: AlertPolicyEnvironmentScopeEnabled(form),
		SourceRefEnabled:   AlertPolicySourceRefEnabled(form),
	}
}

// AlertPolicyShowsNotificationType reports whether the form should submit a notification type selector.
func AlertPolicyShowsNotificationType(form AlertPolicyFormData) bool {
	return AlertPolicyKindForForm(form) == AlertPolicyKindNotificationType
}

// AlertPolicyShowsSeverity reports whether the form should submit a severity selector.
func AlertPolicyShowsSeverity(form AlertPolicyFormData) bool {
	return AlertPolicyKindForForm(form) != AlertPolicyKindSource
}

// AlertPolicyShowsSourceAction reports whether the form should submit a visible source action selector.
func AlertPolicyShowsSourceAction(form AlertPolicyFormData) bool {
	return strings.TrimSpace(form.SourceKind) != ""
}

// AlertPolicyShowsSourceRef reports whether the form should render the source ref selector.
func AlertPolicyShowsSourceRef(form AlertPolicyFormData) bool {
	return AlertPolicySourceRefEnabled(form)
}

// AlertPolicySourceRefEnabled reports whether source_ref should be submitted for the current selector state.
func AlertPolicySourceRefEnabled(form AlertPolicyFormData) bool {
	if strings.TrimSpace(form.SourceKind) == "" {
		return false
	}
	switch api.AlertPolicyScope(strings.TrimSpace(form.Scope)) {
	case api.AlertPolicyScopeOrganization:
		return true
	case api.AlertPolicyScopeProject:
		return strings.TrimSpace(form.ProjectSlug) != ""
	case api.AlertPolicyScopeProjectEnvironment:
		return strings.TrimSpace(form.ProjectSlug) != "" && strings.TrimSpace(form.EnvironmentSlug) != ""
	default:
		return false
	}
}

// AlertPolicySourceRefLabel returns the source-ref label for alert policy forms.
func AlertPolicySourceRefLabel() string {
	return "Source ref"
}

// alertPolicySourceRefOptions returns dynamic ref options while preserving sticky selected values.
func alertPolicySourceRefOptions(form AlertPolicyFormData, sourceRefs []AlertFormOption) []alertFormOption {
	ret := []alertFormOption{{Value: "", Label: "Any source ref"}}
	if AlertPolicySourceRefEnabled(form) {
		for _, option := range sourceRefs {
			if strings.TrimSpace(option.Value) == "" {
				continue
			}
			ret = append(ret, alertFormOption{
				Value: strings.TrimSpace(option.Value),
				Label: strings.TrimSpace(option.Label),
			})
		}
	}
	if value := strings.TrimSpace(form.SourceRef); value != "" && !alertFormOptionsContain(ret, value) {
		ret = append(ret, alertFormOption{Value: value, Label: value})
	}
	return ret
}

// alertPolicyProjectOptions returns project choices while preserving a submitted or persisted slug.
func alertPolicyProjectOptions(form AlertPolicyFormData, projects []AlertFormOption) []alertFormOption {
	ret := []alertFormOption{{Value: "", Label: "Select project"}}
	if AlertPolicyProjectScopeEnabled(form) {
		for _, option := range projects {
			if strings.TrimSpace(option.Value) == "" {
				continue
			}
			ret = append(ret, alertFormOption{
				Value: strings.TrimSpace(option.Value),
				Label: strings.TrimSpace(option.Label),
			})
		}
	}
	if value := strings.TrimSpace(form.ProjectSlug); value != "" && !alertFormOptionsContain(ret, value) {
		ret = append(ret, alertFormOption{Value: value, Label: value})
	}
	return ret
}

// alertPolicyEnvironmentOptions returns project-environment choices while preserving a submitted or persisted slug.
func alertPolicyEnvironmentOptions(form AlertPolicyFormData, environments []AlertFormOption) []alertFormOption {
	label := "Select environment"
	if strings.TrimSpace(form.ProjectSlug) == "" {
		label = "Select project first"
	}
	ret := []alertFormOption{{Value: "", Label: label}}
	if AlertPolicyEnvironmentScopeEnabled(form) && strings.TrimSpace(form.ProjectSlug) != "" {
		for _, option := range environments {
			if strings.TrimSpace(option.Value) == "" {
				continue
			}
			ret = append(ret, alertFormOption{
				Value: strings.TrimSpace(option.Value),
				Label: strings.TrimSpace(option.Label),
			})
		}
	}
	if value := strings.TrimSpace(form.EnvironmentSlug); value != "" && !alertFormOptionsContain(ret, value) {
		ret = append(ret, alertFormOption{Value: value, Label: value})
	}
	return ret
}

// alertFormOptionsContain reports whether options already include value.
func alertFormOptionsContain(options []alertFormOption, value string) bool {
	for _, option := range options {
		if strings.TrimSpace(option.Value) == value {
			return true
		}
	}
	return false
}

// sortAlertFormOptions orders selector options by display label.
func sortAlertFormOptions(options []alertFormOption) {
	sort.SliceStable(options, func(i, j int) bool {
		return strings.ToLower(options[i].Label) < strings.ToLower(options[j].Label)
	})
}

const alertPolicyEnumTokenAPI = "api"

// alertPolicyEnumLabel converts API enum values into compact operator labels.
func alertPolicyEnumLabel(value string) string {
	words := strings.Fields(strings.ReplaceAll(strings.TrimSpace(value), "_", " "))
	for i, word := range words {
		lower := strings.ToLower(word)
		switch lower {
		case alertPolicyEnumTokenAPI:
			words[i] = "API"
		case "sla":
			words[i] = "SLA"
		default:
			words[i] = lower
		}
	}
	if len(words) == 0 {
		return ""
	}
	if words[0] != "API" && words[0] != "SLA" {
		words[0] = strings.ToUpper(words[0][:1]) + words[0][1:]
	}
	return strings.Join(words, " ")
}

// AlertStatusBadgeClass maps enabled state to a compact badge class.
func AlertStatusBadgeClass(enabled bool) string {
	if enabled {
		return eventBadgeSuccess
	}
	return viewBadgeNeutral
}

// AlertStatusLabel maps enabled state to operator display text.
func AlertStatusLabel(enabled bool) string {
	if enabled {
		return "Enabled"
	}
	return "Disabled"
}

// AlertDestinationTypeLabel returns a readable adapter type label.
func AlertDestinationTypeLabel(destinationType api.AlertDestinationType) string {
	for _, option := range AlertDestinationTypeOptions() {
		if option.Value == string(destinationType) {
			return option.Label
		}
	}
	return notificationCapitalizedLabel(string(destinationType))
}

// AlertPolicyScopeLabel returns a readable policy scope label.
func AlertPolicyScopeLabel(scope api.AlertPolicyScope) string {
	for _, option := range AlertPolicyScopeOptions() {
		if option.Value == string(scope) {
			return option.Label
		}
	}
	return notificationCapitalizedLabel(string(scope))
}

// AlertDestinationConfigValue returns a non-secret config value for display.
func AlertDestinationConfigValue(destination api.AlertDestination, key string) string {
	config := alertDestinationConfigMap(destination.NonSecretConfig)
	if len(config) == 0 {
		return ""
	}
	value, ok := config[strings.TrimSpace(key)]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

// AlertDestinationRecipientValues returns all configured email recipients in edit-friendly order.
func AlertDestinationRecipientValues(destination api.AlertDestination) []string {
	config := alertDestinationConfigMap(destination.NonSecretConfig)
	if len(config) == 0 {
		return nil
	}
	if values := alertDestinationRecipientEntries(config["recipients"]); len(values) > 0 {
		return values
	}
	if value := AlertDestinationConfigValue(destination, "to"); value != "" {
		return []string{value}
	}
	return nil
}

// alertDestinationConfigMap decodes the generated OpenAPI union into display-friendly config values.
func alertDestinationConfigMap(config api.AlertDestinationConfig) map[string]any {
	raw, err := json.Marshal(config)
	if err != nil || string(raw) == "null" {
		return map[string]any{}
	}
	ret := map[string]any{}
	if err := json.Unmarshal(raw, &ret); err != nil {
		return map[string]any{}
	}
	return ret
}

// alertDestinationRecipientEntries extracts supported recipient shapes from destination config.
func alertDestinationRecipientEntries(value any) []string {
	switch typed := value.(type) {
	case []any:
		values := make([]string, 0, len(typed))
		for _, entry := range typed {
			if value := alertDestinationRecipientEntry(entry); value != "" {
				values = append(values, value)
			}
		}
		return values
	case string:
		if value := strings.TrimSpace(typed); value != "" {
			return []string{value}
		}
		return nil
	default:
		return nil
	}
}

// alertDestinationRecipientEntry returns the email address from a string or object recipient entry.
func alertDestinationRecipientEntry(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case map[string]any:
		email, ok := typed["email"]
		if !ok || email == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(email))
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

// AlertDestinationRecipientLabel returns the configured recipient summary.
func AlertDestinationRecipientLabel(destination api.AlertDestination) string {
	if destination.Type == api.AlertDestinationTypeSlack {
		return AlertDestinationConfigValue(destination, "connection_slug")
	}
	values := AlertDestinationRecipientValues(destination)
	if len(values) == 0 {
		return "-"
	}
	return strings.Join(values, ", ")
}

// AlertPolicyScopeTargetLabel returns a compact target summary for a policy.
func AlertPolicyScopeTargetLabel(policy api.AlertPolicy) string {
	projectSlug := alertNullableSlugString(policy.ProjectSlug)
	environmentSlug := alertNullableSlugString(policy.EnvironmentSlug)
	switch policy.Scope {
	case api.AlertPolicyScopeOrganization:
		return "Organization"
	case api.AlertPolicyScopeProject:
		if projectSlug == "" {
			return "-"
		}
		return projectSlug
	case api.AlertPolicyScopeProjectEnvironment:
		if projectSlug == "" && environmentSlug == "" {
			return "-"
		}
		if environmentSlug == "" {
			return projectSlug
		}
		if projectSlug == "" {
			return environmentSlug
		}
		return projectSlug + " / " + environmentSlug
	default:
		return "-"
	}
}

// AlertPolicySourceLabel returns the optional source selector summary.
func AlertPolicySourceLabel(policy api.AlertPolicy) string {
	sourceKind := alertNullableAlertPolicySourceKindString(policy.SourceKind)
	sourceAction := alertNullableAlertPolicySourceActionString(policy.SourceAction)
	sourceRef := alertNullableString(policy.SourceRef)
	if sourceKind == "" && sourceAction == "" && sourceRef == "" {
		return "Any source"
	}
	parts := make([]string, 0, 3)
	if sourceKind != "" {
		parts = append(parts, notificationCapitalizedLabel(sourceKind))
	}
	if sourceAction != "" {
		parts = append(parts, notificationCapitalizedLabel(sourceAction))
	}
	if sourceRef != "" {
		parts = append(parts, sourceRef)
	}
	return strings.Join(parts, " / ")
}

// AlertPolicyNotificationTypeLabel returns the optional notification type selector summary.
func AlertPolicyNotificationTypeLabel(policy api.AlertPolicy) string {
	value := alertNullableNotificationTypeString(policy.NotificationType)
	if value == "" {
		return "Any type"
	}
	return notificationTypeLabel(value)
}

// AlertPolicySeverityLabel returns the optional severity selector summary.
func AlertPolicySeverityLabel(policy api.AlertPolicy) string {
	value := alertNullableNotificationSeverityString(policy.Severity)
	if value == "" {
		return "Any severity"
	}
	return notificationCapitalizedLabel(value)
}

// AlertPolicyLifecycleLabel returns the optional lifecycle selector summary.
func AlertPolicyLifecycleLabel(policy api.AlertPolicy) string {
	value := alertNullableNotificationLifecycleStateString(policy.LifecycleState)
	if value == "" {
		return "Any lifecycle"
	}
	return notificationCapitalizedLabel(value)
}

// AlertPolicySeverityBadgeClass maps optional severity selectors to a compact badge class.
func AlertPolicySeverityBadgeClass(policy api.AlertPolicy) string {
	value := alertNullableNotificationSeverityString(policy.Severity)
	if value == "" {
		return "badge badge-neutral badge-outline"
	}
	return notificationSeverityBadgeClass(api.NotificationSeverity(value))
}

// AlertPolicyLifecycleBadgeClass maps optional lifecycle selectors to a compact badge class.
func AlertPolicyLifecycleBadgeClass(policy api.AlertPolicy) string {
	value := alertNullableNotificationLifecycleStateString(policy.LifecycleState)
	if value == "" {
		return "badge badge-neutral badge-outline"
	}
	return notificationLifecycleBadgeClass(api.NotificationLifecycleState(value))
}

// AlertPolicyDestinationLabel formats bound destination slugs.
func AlertPolicyDestinationLabel(slugs []api.Slug) string {
	if len(slugs) == 0 {
		return "-"
	}
	values := make([]string, 0, len(slugs))
	values = append(values, slugs...)
	return strings.Join(values, ", ")
}

// alertNullableAlertPolicySourceKindString returns the string value of an optional policy source kind.
func alertNullableAlertPolicySourceKindString(value *api.AlertPolicySourceKind) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(string(*value))
}

// alertNullableAlertPolicySourceActionString returns the string value of an optional policy source action.
func alertNullableAlertPolicySourceActionString(value *api.AlertPolicySourceAction) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(string(*value))
}

// alertNullableNotificationTypeString returns the string value of an optional notification type.
func alertNullableNotificationTypeString(value *api.NotificationType) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(string(*value))
}

// alertNullableNotificationSeverityString returns the string value of an optional severity.
func alertNullableNotificationSeverityString(value *api.NotificationSeverity) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(string(*value))
}

// alertNullableNotificationLifecycleStateString returns the string value of an optional lifecycle state.
func alertNullableNotificationLifecycleStateString(value *api.NotificationLifecycleState) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(string(*value))
}

// alertNullableString returns a trimmed optional string value.
func alertNullableString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

// AlertPolicyHasDestination reports whether a policy is bound to a destination.
func AlertPolicyHasDestination(policyForm AlertPolicyFormData, destinationSlug api.Slug) bool {
	for _, value := range policyForm.DestinationSlugs {
		if strings.TrimSpace(value) == destinationSlug {
			return true
		}
	}
	return false
}

// AlertDestinationDetailsPanelID returns the DOM id for destination detail panels.
func AlertDestinationDetailsPanelID(destination api.AlertDestination) string {
	return "alert-destination-" + destination.Slug + "-details"
}

// AlertDestinationEditPanelID returns the DOM id for destination edit panels.
func AlertDestinationEditPanelID(destination api.AlertDestination) string {
	return "alert-destination-" + destination.Slug + "-edit"
}

// AlertDestinationDeleteModalID returns the DOM id for destination delete modals.
func AlertDestinationDeleteModalID(destination api.AlertDestination) string {
	return "alert-destination-" + destination.Slug + "-delete"
}

// AlertPolicyDetailsPanelID returns the DOM id for policy detail panels.
func AlertPolicyDetailsPanelID(policy api.AlertPolicy) string {
	return "alert-policy-" + policy.Slug + "-details"
}

// AlertPolicyEditPanelID returns the DOM id for policy edit panels.
func AlertPolicyEditPanelID(policy api.AlertPolicy) string {
	return "alert-policy-" + policy.Slug + "-edit"
}

// AlertPolicyDeleteModalID returns the DOM id for policy delete modals.
func AlertPolicyDeleteModalID(policy api.AlertPolicy) string {
	return "alert-policy-" + policy.Slug + "-delete"
}

// AlertTimestampLabel formats API timestamps consistently for dense rows.
func AlertTimestampLabel(ts api.Timestamp) string {
	if ts.IsZero() {
		return "-"
	}
	return ts.UTC().Format(time.RFC3339)
}

// alertNullableSlugString returns a safe string for optional API slugs.
func alertNullableSlugString(value *api.NullableSlug) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

// alertSlugStrings converts API slugs to form string values.
func alertSlugStrings(values []api.Slug) []string {
	ret := make([]string, 0, len(values))
	ret = append(ret, values...)
	return ret
}

func AlertConnectionListed(connections []api.NotificationConnection, selected string) bool {
	for _, connection := range connections {
		if connection.Slug == selected {
			return true
		}
	}
	return false
}

func AlertDestinationTargetLabel(destination api.AlertDestination, connections []api.NotificationConnection) string {
	if destination.Type != api.AlertDestinationTypeSlack {
		return AlertDestinationRecipientLabel(destination)
	}
	selected := AlertDestinationConfigValue(destination, "connection_slug")
	for _, connection := range connections {
		if connection.Slug == selected {
			return connection.Name + " · " + NotificationConnectionDescription(connection) + " (" + string(connection.Status) + ")"
		}
	}
	return selected + " (unavailable)"
}

func AlertDestinationCanTest(destination api.AlertDestination, connections []api.NotificationConnection) bool {
	if !destination.Enabled {
		return false
	}
	if destination.Type != api.AlertDestinationTypeSlack {
		return true
	}
	selected := AlertDestinationConfigValue(destination, "connection_slug")
	for _, connection := range connections {
		if connection.Slug == selected {
			return connection.Status == api.CredentialStatusActive
		}
	}
	return false
}

// AlertJourneyHref carries the original inbox filters through configuration navigation.
func AlertJourneyHref(orgSlug string, tab AlertTab, returnTo, selected string) string {
	values := url.Values{"tab": {string(tab)}}
	if returnTo != "" {
		values.Set("notification_return", returnTo)
	}
	if selected != "" {
		if tab == AlertTabPolicies {
			values.Set("policy_slug", selected)
		} else {
			values.Set("destination_slug", selected)
		}
	}
	return "/orgs/" + orgSlug + "/alerts?" + values.Encode()
}

func AlertSetupHref(org string, tab AlertTab, form AlertPolicyFormData) string {
	href, _ := url.Parse(AlertJourneyHref(org, tab, form.NotificationReturn, ""))
	if form.NotificationReturn != "" {
		values := href.Query()
		values.Set("notification_type", form.NotificationType)
		values.Set("project_slug", form.ProjectSlug)
		values.Set("environment_slug", form.EnvironmentSlug)
		href.RawQuery = values.Encode()
	}
	return href.String()
}

func AlertPolicyNotificationsHref(org string, policy api.AlertPolicy) string {
	filters := NotificationFilters{
		NotificationType: alertNullableNotificationTypeString(policy.NotificationType),
		Severity:         alertNullableNotificationSeverityString(policy.Severity),
	}
	if policy.LifecycleState != nil && *policy.LifecycleState == api.NotificationLifecycleStateResolved {
		filters.State = NotificationFilterStateResolved
	}
	return NotificationsListHref(org, "", "", filters)
}
