package executionbroker

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
)

type testCredentialHandoffRecorder struct{}

func (testCredentialHandoffRecorder) Record(_ context.Context, _ slug.Slug, _ model.EventActor, event eventrecorder.Event) (*model.Event, error) {
	_, err := event.EventRecord()
	return nil, err
}
