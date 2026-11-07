package notificationsvc

import (
	"context"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	reponotifications "github.com/Braidplane/braidplane-core/postgres/notifications"
)

const (
	alertDeliveryTerminalJobState     = "completed"
	alertDeliveryFailedJobState       = "failed"
	maxAlertDeliveryAttemptsPerRecord = 10
)

// ListAlertDeliveryAttempts returns recent alert delivery diagnostics for one
// notification record.
func (s *service) ListAlertDeliveryAttempts(
	ctx context.Context,
	orgSlug slug.Slug,
	recordSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[*model.AlertDeliveryAttempt], error) {
	if orgSlug == "" {
		return model.ListResponse[*model.AlertDeliveryAttempt]{}, ErrInvalidInput.Detail("organization slug is required")
	}
	if recordSlug == "" {
		return model.ListResponse[*model.AlertDeliveryAttempt]{}, ErrInvalidInput.Detail("notification slug is required")
	}

	normalized, queryReq := model.PrepareListQuery(req, model.ListSortCreatedAt)

	ret, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) ([]*model.AlertDeliveryAttempt, error) {
		if _, findErr := s.repo.FindRecordBySlug(ctx, db, orgSlug, recordSlug); findErr != nil {
			return nil, xerror.Wrapf("find notification record for alert delivery attempts: %w", findErr)
		}

		attempts, listErr := s.repo.ListAlertDeliveryAttemptsForRecord(ctx, db, orgSlug, recordSlug, queryReq)
		if listErr != nil {
			return nil, xerror.Wrapf("list alert delivery attempts: %w", listErr)
		}

		converted := make([]*model.AlertDeliveryAttempt, 0, len(attempts))
		for _, attempt := range attempts {
			item, transformErr := toAlertDeliveryAttempt(attempt)
			if transformErr != nil {
				return nil, transformErr
			}
			converted = append(converted, item)
		}
		return converted, nil
	})
	if err != nil {
		return model.ListResponse[*model.AlertDeliveryAttempt]{}, err
	}

	ret, hasMore := model.FinalizePage(ret, normalized)
	nextCursor, prevCursor := model.BuildBoundaryCursors(
		ret,
		normalized,
		hasMore,
		func(item *model.AlertDeliveryAttempt, _ string) (value, slugValue string) {
			return item.CreatedAt.Format(time.RFC3339Nano), item.Slug.String()
		},
	)

	return model.ListResponse[*model.AlertDeliveryAttempt]{
		Items:      ret,
		HasMore:    hasMore,
		NextCursor: nextCursor,
		PrevCursor: prevCursor,
	}, nil
}

// ListRecentAlertDeliveryAttemptsForRecords returns capped recent alert
// delivery diagnostics for multiple notification records.
func (s *service) ListRecentAlertDeliveryAttemptsForRecords(
	ctx context.Context,
	orgSlug slug.Slug,
	recordSlugs []slug.Slug,
	limitPerRecord int,
) ([]*model.AlertDeliveryAttempt, error) {
	if orgSlug == "" {
		return nil, ErrInvalidInput.Detail("organization slug is required")
	}
	if len(recordSlugs) == 0 {
		return []*model.AlertDeliveryAttempt{}, nil
	}
	if limitPerRecord <= 0 {
		return nil, ErrInvalidInput.Detail("limit per notification must be greater than zero")
	}
	if limitPerRecord > maxAlertDeliveryAttemptsPerRecord {
		return nil, ErrInvalidInput.Detail("limit per notification must be 10 or less")
	}

	ret, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) ([]*model.AlertDeliveryAttempt, error) {
		attempts, listErr := s.repo.ListRecentAlertDeliveryAttemptsForRecords(ctx, db, orgSlug, recordSlugs, limitPerRecord)
		if listErr != nil {
			return nil, xerror.Wrapf("list recent alert delivery attempts: %w", listErr)
		}

		converted := make([]*model.AlertDeliveryAttempt, 0, len(attempts))
		for _, attempt := range attempts {
			item, transformErr := toAlertDeliveryAttempt(attempt)
			if transformErr != nil {
				return nil, transformErr
			}
			converted = append(converted, item)
		}
		return converted, nil
	})
	if err != nil {
		return nil, err
	}
	return ret, nil
}

// MarkAlertDeliveryAttemptSent records successful provider execution for an alert delivery attempt.
func (s *service) MarkAlertDeliveryAttemptSent(ctx context.Context, params MarkAlertDeliveryAttemptSentParams) error {
	if err := validate.Default().Struct(params); err != nil {
		return ErrInvalidInput.With(err)
	}

	metadata, err := normalizeSafeMap(params.ProviderResponseMetadata)
	if err != nil {
		return err
	}

	_, err = postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (struct{}, error) {
		_, markErr := s.repo.MarkAlertDeliveryAttemptSent(ctx, tx, reponotifications.MarkAlertDeliveryAttemptSentParams{
			OrgSlug:                  params.OrgSlug,
			AttemptSlug:              params.AttemptSlug,
			ProviderAttempt:          params.ProviderAttempt,
			JobState:                 alertDeliveryTerminalJobState,
			ProviderResponseMetadata: metadata,
			SentAt:                   s.clk.Now().UTC(),
		})
		if markErr != nil {
			return struct{}{}, xerror.Wrapf("mark alert delivery attempt sent: %w", markErr)
		}
		return struct{}{}, nil
	})
	return err
}

// MarkAlertDeliveryAttemptFailed records failed provider execution for an alert delivery attempt.
func (s *service) MarkAlertDeliveryAttemptFailed(ctx context.Context, params MarkAlertDeliveryAttemptFailedParams) error {
	if err := validate.Default().Struct(params); err != nil {
		return ErrInvalidInput.With(err)
	}

	metadata, err := normalizeSafeMap(params.ProviderResponseMetadata)
	if err != nil {
		return err
	}
	errorCategory := optionalTrimmedString(params.ErrorCategory)
	errorMessage := optionalTrimmedString(params.ErrorMessage)

	_, err = postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (struct{}, error) {
		_, markErr := s.repo.MarkAlertDeliveryAttemptFailed(ctx, tx, reponotifications.MarkAlertDeliveryAttemptFailedParams{
			OrgSlug:                  params.OrgSlug,
			AttemptSlug:              params.AttemptSlug,
			ProviderAttempt:          params.ProviderAttempt,
			JobState:                 alertDeliveryFailedJobState,
			ProviderResponseMetadata: metadata,
			ErrorCategory:            errorCategory,
			ErrorMessage:             errorMessage,
			FailedAt:                 s.clk.Now().UTC(),
		})
		if markErr != nil {
			return struct{}{}, xerror.Wrapf("mark alert delivery attempt failed: %w", markErr)
		}
		return struct{}{}, nil
	})
	return err
}

// optionalTrimmedString converts blank external text to nil for nullable diagnostics fields.
func optionalTrimmedString(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}
