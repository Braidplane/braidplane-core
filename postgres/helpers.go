package postgres

import (
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

// FromNullableText converts pgtype.Text to *string, returning nil if not valid.
func FromNullableText(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

// ToNullableText converts *string to pgtype.Text, returning invalid pgtype.Text if nil.
func ToNullableText(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{Valid: false}
	}
	return pgtype.Text{String: *s, Valid: true}
}

// ToText converts a string to pgtype.Text.
func ToText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{Valid: false}
	}
	return pgtype.Text{String: s, Valid: true}
}

// FromNullableInt64 converts nullable database identities and revisions.
func FromNullableInt64(i pgtype.Int8) *int64 {
	if !i.Valid {
		return nil
	}
	return &i.Int64
}

func ToNullableInt64(i *int64) pgtype.Int8 {
	if i == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *i, Valid: true}
}

// FromNullableInt32 converts pgtype.Int4 to *int32, returning nil if not valid.
func FromNullableInt32(i pgtype.Int4) *int32 {
	if !i.Valid {
		return nil
	}
	return &i.Int32
}

// ToNullableInt32 converts *int32 to pgtype.Int4, returning invalid pgtype.Int4 if nil.
func ToNullableInt32(i *int32) pgtype.Int4 {
	if i == nil {
		return pgtype.Int4{Valid: false}
	}
	return pgtype.Int4{Int32: *i, Valid: true}
}

// FromNullableTimestamptz converts pgtype.Timestamptz to *time.Time, returning nil if not valid.
func FromNullableTimestamptz(ts pgtype.Timestamptz) *time.Time {
	if !ts.Valid {
		return nil
	}
	return &ts.Time
}

// ToTimestamptz converts time.Time to pgtype.Timestamptz.
func ToTimestamptz(ts time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: ts, Valid: true}
}

// ToNullableTimestamptz converts *time.Time to pgtype.Timestamptz, returning invalid if nil.
func ToNullableTimestamptz(ts *time.Time) pgtype.Timestamptz {
	if ts == nil {
		return pgtype.Timestamptz{Valid: false}
	}
	return ToTimestamptz(*ts)
}

// SlugToText converts a slug.Slug to pgtype.Text.
func SlugToText(s slug.Slug) pgtype.Text {
	return pgtype.Text{String: string(s), Valid: true}
}

// OptionalSlugToText converts a *slug.Slug to pgtype.Text, returning invalid if nil.
func OptionalSlugToText(s *slug.Slug) pgtype.Text {
	if s == nil {
		return pgtype.Text{Valid: false}
	}
	return SlugToText(*s)
}
