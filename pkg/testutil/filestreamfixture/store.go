// Package filestreamfixture provides retained input for small hermetic connector
// tests. Its in-memory storage must not be used as capacity evidence.
package filestreamfixture

import (
	"bytes"
	"context"
	"io"
	"sync"
	"sync/atomic"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
)

type Store struct {
	mu    sync.Mutex
	files map[string][]byte
	Opens atomic.Int32
}

func New() *Store { return &Store{files: make(map[string][]byte)} }
func (s *Store) Retain(_ context.Context, maxBytes int64, produce filestream.Producer) (*filepayload.Reference, *filestream.Metadata, error) {
	var buffer bytes.Buffer
	metadata, err := produce(&buffer)
	if err != nil {
		return nil, nil, err
	}
	ref := &filepayload.Reference{ID: uuid.New().String(), SizeBytes: metadata.SizeBytes, SHA256: metadata.SHA256}
	if _, err := filestream.ParseReference(*ref, maxBytes); err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	s.files[ref.ID] = bytes.Clone(buffer.Bytes())
	s.mu.Unlock()
	return ref, metadata, nil
}

func (s *Store) Open(_ context.Context, ref filepayload.Reference, maxBytes int64) (io.ReadCloser, error) {
	if _, err := filestream.ParseReference(ref, maxBytes); err != nil {
		return nil, err
	}
	s.mu.Lock()
	content, ok := s.files[ref.ID]
	s.mu.Unlock()
	if !ok || int64(len(content)) != ref.SizeBytes {
		return nil, filestream.ErrUnavailable
	}
	s.Opens.Add(1)
	return io.NopCloser(bytes.NewReader(content)), nil
}
