package postgres

import (
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// BuildCursorValues parses an encoded cursor into SQLC-friendly typed values.
func BuildCursorValues(
	cursor, sortBy string,
) (pgtype.Text, pgtype.Timestamptz, pgtype.Text, error) {
	cursorSlug := pgtype.Text{Valid: false}
	cursorTimestamp := pgtype.Timestamptz{Valid: false}
	cursorText := pgtype.Text{Valid: false}

	if cursor == "" {
		return cursorSlug, cursorTimestamp, cursorText, nil
	}

	separator := strings.LastIndex(cursor, "_")
	switch sortBy {
	case model.ListSortCreatedAt, model.ListSortUpdatedAt, model.ListSortExecutedAt, model.ListSortOccurredAt, model.ListSortScheduledAt, model.ListSortLastSeenAt:
		separator = strings.Index(cursor, "_")
	}
	if separator <= 0 || separator >= len(cursor)-1 {
		return cursorSlug, cursorTimestamp, cursorText, xerror.InvalidInput(xerror.Cursor).
			Detail("cursor must contain both a value and slug")
	}

	value := cursor[:separator]
	slug := cursor[separator+1:]
	cursorSlug = pgtype.Text{String: slug, Valid: true}

	switch sortBy {
	case model.ListSortCreatedAt, model.ListSortUpdatedAt, model.ListSortExecutedAt, model.ListSortOccurredAt, model.ListSortExpiresAt, model.ListSortScheduledAt, model.ListSortLastSeenAt:
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return pgtype.Text{
					Valid: false,
				}, pgtype.Timestamptz{
					Valid: false,
				}, pgtype.Text{
					Valid: false,
				}, xerror.InvalidInput(xerror.Cursor).
					Detailf("cursor timestamp is invalid: %w", err)
		}
		cursorTimestamp = pgtype.Timestamptz{Time: parsed, Valid: true}
	default:
		cursorText = pgtype.Text{String: value, Valid: true}
	}

	return cursorSlug, cursorTimestamp, cursorText, nil
}
