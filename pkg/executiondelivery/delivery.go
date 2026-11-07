package executiondelivery

//go:generate -command enumer go tool -modfile=../../tools/go.mod enumer
//go:generate enumer -type=ProcessDisposition -text -linecomment -output=process_disposition_enumer.go

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/uuid"
)

type ProcessDisposition uint8

const (
	ProcessDone    ProcessDisposition = iota // done
	ProcessRetryAt                           // retry_at
)

type ProcessResult struct {
	Disposition ProcessDisposition
	RetryAt     time.Time
}

type Processor interface {
	Process(context.Context, uuid.UUID) (ProcessResult, error)
}

type requestKey struct{}

// WithRequest binds executor-only lookups to a claimed encrypted envelope.
func WithRequest(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, requestKey{}, id)
}

func Request(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(requestKey{}).(uuid.UUID)
	return id, ok && id != (uuid.UUID{})
}
