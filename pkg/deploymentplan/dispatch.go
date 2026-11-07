package deploymentplan

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type (
	dispatchKey struct{}
	stepKey     struct{}
)

type Admission struct {
	Step                slug.Slug `json:"step"`
	DataSource          slug.Slug `json:"data_source"`
	CredentialReference string    `json:"credential_reference"`
}

type dispatchScope struct {
	mu         sync.Mutex
	admissions []Admission
	org        slug.Slug
	targets    []Target
}

func WithDispatch(ctx context.Context, org slug.Slug, targets []Target) context.Context {
	return context.WithValue(ctx, dispatchKey{}, &dispatchScope{org: org, targets: slices.Clone(targets)})
}

// DispatchConfig requires every dispatched source to belong to the attempt.
func DispatchConfig(ctx context.Context, org, source slug.Slug) (string, error) {
	scope, ok := ctx.Value(dispatchKey{}).(*dispatchScope)
	if !ok {
		return "", nil
	}
	if scope.org != org {
		return "", ErrInvalid
	}
	var digest string
	for _, target := range scope.targets {
		if target.DataSource != source {
			continue
		}
		if digest != "" && digest != target.ConfigDigest {
			return "", ErrInvalid
		}
		digest = target.ConfigDigest
	}
	if digest == "" {
		return "", ErrInvalid
	}
	return digest, nil
}

func WithStep(ctx context.Context, step slug.Slug) context.Context {
	return context.WithValue(ctx, stepKey{}, step)
}

// Admit records a lifecycle reference, never the credential's value or its hash.
func Admit(ctx context.Context, source slug.Slug, reference string) error {
	scope, ok := ctx.Value(dispatchKey{}).(*dispatchScope)
	if !ok {
		return nil
	}
	credential, revision, ok := strings.Cut(reference, "/")
	if !ok {
		return ErrInvalid
	}
	if _, err := slug.New(credential); err != nil {
		return ErrInvalid.With(err)
	}
	if _, err := time.Parse(time.RFC3339Nano, revision); err != nil {
		return ErrInvalid.With(err)
	}
	step, ok := ctx.Value(stepKey{}).(slug.Slug)
	if !ok || step == "" {
		return ErrInvalid
	}
	scope.mu.Lock()
	defer scope.mu.Unlock()
	scope.admissions = append(scope.admissions, Admission{Step: step, DataSource: source, CredentialReference: reference})
	return nil
}

func Admissions(ctx context.Context) []Admission {
	scope, ok := ctx.Value(dispatchKey{}).(*dispatchScope)
	if !ok {
		return nil
	}
	scope.mu.Lock()
	defer scope.mu.Unlock()
	return slices.Clone(scope.admissions)
}
