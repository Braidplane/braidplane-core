package views

import api "github.com/Braidplane/braidplane-core/api-schema/gen/client"

type NotificationConnectionFormData struct {
	Name string
	Slug string
}

func NotificationConnectionDescription(connection api.NotificationConnection) string {
	switch connection.Provider {
	case api.NotificationProviderSlack:
		metadata, err := connection.Metadata.AsSlackNotificationConnectionMetadata()
		if err != nil || metadata.WorkspaceName == "" {
			return ""
		}
		return metadata.WorkspaceName + " · " + metadata.ChannelName
	default:
		return ""
	}
}
