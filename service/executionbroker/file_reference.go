package executionbroker

import "github.com/Braidplane/braidplane-core/pkg/connector/filestream"

func validateResultFileReference(payload *executionResultRuntimePayload, maxFileBytes int64) error {
	if payload == nil || payload.FileReference == nil {
		return nil
	}
	_, err := filestream.ParseReference(*payload.FileReference, maxFileBytes)
	return err
}
