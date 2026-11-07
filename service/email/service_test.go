package email

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wneessen/go-mail"

	"github.com/Braidplane/braidplane-core/config"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/i18n"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type recordingMailClient struct {
	messages []*mail.Msg
	err      error
}

func (c *recordingMailClient) DialAndSendWithContext(_ context.Context, messages ...*mail.Msg) error {
	c.messages = append(c.messages, messages...)
	return c.err
}

type missingLabelLocalizer struct{}

func (missingLabelLocalizer) Localize(messageID string, _ map[string]any) string {
	return messageID
}

func (missingLabelLocalizer) LocalizeWithPlural(messageID string, _ any, _ map[string]any) string {
	return messageID
}

func TestNotificationEmailTemplateDataLocalizesLabels(t *testing.T) {
	t.Parallel()

	bundle, err := i18n.NewBundle("en")
	require.NoError(t, err)
	localizer := bundle.NewLocalizer("pl")

	got := notificationEmailTemplateData(localizer, model.NotificationEmail{
		OrganizationName: "Acme",
		RecordSlug:       must.Slug("pipeline-run-failed"),
		NotificationType: model.NotificationTypePipelineRunFailed,
		Severity:         model.NotificationSeverityCritical,
		Summary:          "contract failed",
		SourceEntityType: model.EventEntityTypePipelineRun,
		SourceAction:     model.EventActionExecute,
		OccurrenceCount:  2,
	})

	require.Equal(t, "nieudane uruchomienie potoku", got["NotificationType"])
	require.Equal(t, "krytyczne", got["Severity"])
	require.Equal(t, "uruchomienie potoku", got["SourceEntityType"])
	require.Equal(t, "wykonanie", got["SourceAction"])
	require.NotEqual(t, model.NotificationTypePipelineRunFailed.String(), got["NotificationType"])
	require.NotEqual(t, model.NotificationSeverityCritical.String(), got["Severity"])
	require.NotEqual(t, model.EventEntityTypePipelineRun.String(), got["SourceEntityType"])
	require.NotEqual(t, model.EventActionExecute.String(), got["SourceAction"])
}

func TestNotificationEmailTemplateDataUsesExplicitTypeLabel(t *testing.T) {
	t.Parallel()

	bundle, err := i18n.NewBundle("en")
	require.NoError(t, err)
	localizer := bundle.NewLocalizer("en")

	got := notificationEmailTemplateData(localizer, model.NotificationEmail{
		OrganizationName:      "Acme",
		RecordSlug:            must.Slug("alert-destination-test"),
		NotificationType:      model.NotificationTypePipelineRunFailed,
		NotificationTypeLabel: "alert destination test",
		Severity:              model.NotificationSeverityInfo,
		Summary:               "Test notification for alert destination ops-email.",
		SourceEntityType:      model.EventEntityTypeOrganization,
		SourceAction:          model.EventActionTestConnection,
		OccurrenceCount:       1,
	})

	require.Equal(t, "alert destination test", got["NotificationType"])
}

func TestNotificationEmailTemplateDataFallsBackWhenLabelIsMissing(t *testing.T) {
	t.Parallel()

	localizer := missingLabelLocalizer{}

	require.Equal(t, "pipeline.run_failed", localizedNotificationType(localizer, model.NotificationTypePipelineRunFailed))
	require.Equal(t, "critical", localizedNotificationSeverity(localizer, model.NotificationSeverityCritical))
	require.Equal(t, "pipeline_run", localizedEventEntityType(localizer, model.EventEntityTypePipelineRun))
	require.Equal(t, "execute", localizedEventAction(localizer, model.EventActionExecute))
}

func TestServiceNotification(t *testing.T) {
	t.Parallel()

	sendErr := fmt.Errorf("smtp unavailable")

	tests := []struct {
		name      string
		clientErr error
		email     model.NotificationEmail
		assert    func(t *testing.T, client *recordingMailClient, err error)
	}{
		{
			name: "SendsLocalizedEmail",
			email: model.NotificationEmail{
				OrganizationName: "Acme",
				RecordSlug:       must.Slug("pipeline-run-failed"),
				NotificationType: model.NotificationTypePipelineRunFailed,
				Severity:         model.NotificationSeverityCritical,
				Summary:          "contract failed",
				SourceEntityType: model.EventEntityTypePipelineRun,
				SourceAction:     model.EventActionExecute,
				OccurrenceCount:  2,
			},
			assert: func(t *testing.T, client *recordingMailClient, err error) {
				t.Helper()

				require.NoError(t, err)
				require.Len(t, client.messages, 1)
				message := client.messages[0]
				require.Equal(t, []string{"<ops@example.com>"}, message.GetToString())
				require.Equal(t, []string{"<no-reply@example.com>"}, message.GetFromString())
				require.Equal(t, []string{"[critical] pipeline run failed in Acme"}, message.GetGenHeader(mail.HeaderSubject))
				parts := message.GetParts()
				require.Len(t, parts, 1)
				body, err := parts[0].GetContent()
				require.NoError(t, err)
				bodyText := string(body)
				require.Contains(t, bodyText, "Acme has a critical pipeline run failed notification")
				require.Contains(t, bodyText, "contract failed")
				require.Contains(t, bodyText, "Occurrences: 2")
				require.Contains(t, bodyText, "Notification: pipeline-run-failed")
			},
		},
		{
			name:      "WrapsSendError",
			clientErr: sendErr,
			email: model.NotificationEmail{
				OrganizationName: "Acme",
				RecordSlug:       must.Slug("pipeline-run-failed"),
				NotificationType: model.NotificationTypePipelineRunFailed,
				Severity:         model.NotificationSeverityCritical,
				Summary:          strings.Repeat("failed ", 2),
				SourceEntityType: model.EventEntityTypePipelineRun,
				SourceAction:     model.EventActionExecute,
				OccurrenceCount:  1,
			},
			assert: func(t *testing.T, _ *recordingMailClient, err error) {
				t.Helper()

				require.ErrorIs(t, err, xerror.Unexpected())
				require.ErrorIs(t, err, sendErr)
			},
		},
		{
			name: "SendsExplicitNotificationTypeLabel",
			email: model.NotificationEmail{
				OrganizationName:      "Acme",
				RecordSlug:            must.Slug("alert-destination-test"),
				NotificationType:      model.NotificationTypePipelineRunFailed,
				NotificationTypeLabel: "alert destination test",
				Severity:              model.NotificationSeverityInfo,
				Summary:               "Test notification for alert destination ops-email.",
				SourceEntityType:      model.EventEntityTypeOrganization,
				SourceAction:          model.EventActionTestConnection,
				OccurrenceCount:       1,
			},
			assert: func(t *testing.T, client *recordingMailClient, err error) {
				t.Helper()

				require.NoError(t, err)
				require.Len(t, client.messages, 1)
				message := client.messages[0]
				require.Equal(t, []string{"[info] alert destination test in Acme"}, message.GetGenHeader(mail.HeaderSubject))
				parts := message.GetParts()
				require.Len(t, parts, 1)
				body, err := parts[0].GetContent()
				require.NoError(t, err)
				bodyText := string(body)
				require.Contains(t, bodyText, "Acme has a info alert destination test notification")
				require.Contains(t, bodyText, "Test notification for alert destination ops-email.")
				require.NotContains(t, bodyText, "pipeline run failed")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			bundle, err := i18n.NewBundle("en")
			require.NoError(t, err)
			client := &recordingMailClient{err: tc.clientErr}
			svc := service{
				client:      client,
				bundle:      bundle,
				noReplyAddr: "no-reply@example.com",
			}

			err = svc.Notification(context.Background(), model.NotificationEmailRecipient{
				Email:  "ops@example.com",
				Locale: "en",
			}, tc.email)

			tc.assert(t, client, err)
		})
	}
}

func TestNewServiceRejectsPartialAuthentication(t *testing.T) {
	_, err := NewService(config.SMTP{Host: "smtp.customer.example.test:587", User: "synthetic-user"}, nil)
	require.ErrorContains(t, err, "smtp.user and smtp.password must be configured together")
}

func TestServiceMessageSendsPreparedText(t *testing.T) {
	t.Parallel()
	client := &recordingMailClient{}
	svc := service{client: client, noReplyAddr: "no-reply@example.com"}
	require.NoError(t, svc.Message(context.Background(), &model.User{Email: "alice@example.com"}, "Synthetic subject", "Synthetic body"))
	require.Len(t, client.messages, 1)
	require.Equal(t, []string{"Synthetic subject"}, client.messages[0].GetGenHeader(mail.HeaderSubject))
	parts := client.messages[0].GetParts()
	require.Len(t, parts, 1)
	body, err := parts[0].GetContent()
	require.NoError(t, err)
	require.Equal(t, "Synthetic body", string(body))
}
