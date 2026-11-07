package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestBuildCursorValues_ParsesRFC3339Nano(t *testing.T) {
	t.Parallel()

	cursor := "2026-03-16T12:34:56.123456789Z_run-001"
	cursorSlug, cursorTimestamp, cursorText, err := BuildCursorValues(
		cursor,
		model.ListSortCreatedAt,
	)
	require.NoError(t, err)
	require.True(t, cursorSlug.Valid)
	require.Equal(t, "run-001", cursorSlug.String)
	require.True(t, cursorTimestamp.Valid)
	require.Equal(t, 123456789, cursorTimestamp.Time.UTC().Nanosecond())
	require.False(t, cursorText.Valid)
}

func TestBuildCursorValues_ParsesTimestampCursorWithUnderscoreSlug(t *testing.T) {
	t.Parallel()

	cursor := "2026-03-16T12:34:56.123456789Z_s3_prefix"
	cursorSlug, cursorTimestamp, cursorText, err := BuildCursorValues(
		cursor,
		model.ListSortUpdatedAt,
	)
	require.NoError(t, err)
	require.True(t, cursorSlug.Valid)
	require.Equal(t, "s3_prefix", cursorSlug.String)
	require.True(t, cursorTimestamp.Valid)
	require.Equal(t, 123456789, cursorTimestamp.Time.UTC().Nanosecond())
	require.False(t, cursorText.Valid)
}

func TestBuildCursorValues_InvalidTimestampReturnsEmptyValues(t *testing.T) {
	t.Parallel()

	cursor := "not-a-timestamp_run-001"
	cursorSlug, cursorTimestamp, cursorText, err := BuildCursorValues(
		cursor,
		model.ListSortCreatedAt,
	)
	require.ErrorIs(t, err, xerror.InvalidInput(xerror.Cursor))
	require.False(t, cursorSlug.Valid)
	require.False(t, cursorTimestamp.Valid)
	require.False(t, cursorText.Valid)
}

func TestBuildCursorValues_InvalidCursorShapeReturnsError(t *testing.T) {
	t.Parallel()

	cursorSlug, cursorTimestamp, cursorText, err := BuildCursorValues(
		"missing-separator",
		model.ListSortCreatedAt,
	)
	require.ErrorIs(t, err, xerror.InvalidInput(xerror.Cursor))
	require.False(t, cursorSlug.Valid)
	require.False(t, cursorTimestamp.Valid)
	require.False(t, cursorText.Valid)
}
