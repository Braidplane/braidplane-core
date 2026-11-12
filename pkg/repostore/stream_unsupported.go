//go:build !linux && !darwin

package repostore

import (
	"context"
	"io"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
)

func streamRepositoryFile(context.Context, string, string, int64, io.Writer) (*filestream.Metadata, error) {
	return nil, ErrRepositorySetup.Detail("secure repository file streaming is unsupported on this platform")
}

func writeRepositoryStream(context.Context, string, string, io.Reader, filepayload.Reference, bool) error {
	return ErrRepositorySetup.Detail("secure repository file streaming is unsupported on this platform")
}
