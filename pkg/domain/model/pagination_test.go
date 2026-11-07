package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeListRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       ListRequest
		defaultSort string
		want        ListRequest
	}{
		{
			name:        "AppliesDefaultsWhenValuesAreMissing",
			input:       ListRequest{},
			defaultSort: ListSortCreatedAt,
			want: ListRequest{
				Limit:     20,
				SortBy:    ListSortCreatedAt,
				Order:     ListOrderDesc,
				Direction: ListDirectionNext,
			},
		},
		{
			name: "ClampsLimitAndNormalizesInvalidFlags",
			input: ListRequest{
				Limit:     999,
				SortBy:    ListSortName,
				Order:     "invalid",
				Direction: "invalid",
			},
			defaultSort: ListSortCreatedAt,
			want: ListRequest{
				Limit:     100,
				SortBy:    ListSortName,
				Order:     ListOrderDesc,
				Direction: ListDirectionNext,
			},
		},
		{
			name: "KeepsValidInputValues",
			input: ListRequest{
				Limit:     5,
				Cursor:    "2026-01-01T00:00:00Z_org-1",
				SortBy:    ListSortUpdatedAt,
				Order:     ListOrderAsc,
				Direction: ListDirectionPrev,
			},
			defaultSort: ListSortCreatedAt,
			want: ListRequest{
				Limit:     5,
				Cursor:    "2026-01-01T00:00:00Z_org-1",
				SortBy:    ListSortUpdatedAt,
				Order:     ListOrderAsc,
				Direction: ListDirectionPrev,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := NormalizeListRequest(tc.input, tc.defaultSort)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestReverseOrder(t *testing.T) {
	t.Parallel()

	assert.Equal(t, ListOrderDesc, ReverseOrder(ListOrderAsc))
	assert.Equal(t, ListOrderAsc, ReverseOrder(ListOrderDesc))
	assert.Equal(t, ListOrderAsc, ReverseOrder("unexpected"))
}

func TestReverseInPlace(t *testing.T) {
	t.Parallel()

	items := []int{1, 2, 3, 4}
	ReverseInPlace(items)
	assert.Equal(t, []int{4, 3, 2, 1}, items)
}

func TestPrepareListQuery(t *testing.T) {
	t.Parallel()

	normalized, queryReq := PrepareListQuery(ListRequest{
		Limit:     2,
		SortBy:    ListSortCreatedAt,
		Order:     ListOrderDesc,
		Direction: ListDirectionPrev,
	}, ListSortCreatedAt)

	assert.Equal(t, 2, normalized.Limit)
	assert.Equal(t, ListOrderDesc, normalized.Order)
	assert.Equal(t, ListOrderAsc, queryReq.Order)
	assert.Equal(t, 3, queryReq.Limit)
}

func TestFinalizePage(t *testing.T) {
	t.Parallel()

	t.Run("TrimsToLimitAndSetsHasMore", func(t *testing.T) {
		t.Parallel()

		items, hasMore := FinalizePage(
			[]string{"a", "b", "c"},
			ListRequest{Limit: 2, Direction: ListDirectionNext},
		)
		assert.True(t, hasMore)
		assert.Equal(t, []string{"a", "b"}, items)
	})

	t.Run("ReversesItemsForPrevDirection", func(t *testing.T) {
		t.Parallel()

		items, hasMore := FinalizePage(
			[]string{"a", "b", "c"},
			ListRequest{Limit: 2, Direction: ListDirectionPrev},
		)
		assert.True(t, hasMore)
		assert.Equal(t, []string{"b", "a"}, items)
	})
}

func TestBuildBoundaryCursors(t *testing.T) {
	t.Parallel()

	type item struct {
		value string
		slug  string
	}

	cursorParts := func(it item, _ string) (string, string) {
		return it.value, it.slug
	}

	t.Run("ReturnsNilCursorsForEmptyItems", func(t *testing.T) {
		t.Parallel()

		next, prev := BuildBoundaryCursors([]item{}, ListRequest{}, false, cursorParts)
		assert.Nil(t, next)
		assert.Nil(t, prev)
	})

	t.Run("BuildsNextAndPrevWhenApplicable", func(t *testing.T) {
		t.Parallel()

		items := []item{{value: "v1", slug: "s1"}, {value: "v2", slug: "s2"}}
		next, prev := BuildBoundaryCursors(
			items,
			ListRequest{Cursor: "existing-cursor", SortBy: ListSortCreatedAt},
			true,
			cursorParts,
		)

		require.NotNil(t, next)
		require.NotNil(t, prev)
		assert.Equal(t, "v2_s2", *next)
		assert.Equal(t, "v1_s1", *prev)
	})

	t.Run("BuildsPrevWithoutIncomingCursorForPrevDirection", func(t *testing.T) {
		t.Parallel()

		items := []item{{value: "v1", slug: "s1"}}
		next, prev := BuildBoundaryCursors(
			items,
			ListRequest{Direction: ListDirectionPrev, SortBy: ListSortCreatedAt},
			false,
			cursorParts,
		)

		assert.Nil(t, next)
		require.NotNil(t, prev)
		assert.Equal(t, "v1_s1", *prev)
	})
}

func TestEncodeCursor(t *testing.T) {
	t.Parallel()

	encoded := EncodeCursor("2026-01-01T00:00:00Z", "org-1")
	require.NotNil(t, encoded)
	assert.Equal(t, "2026-01-01T00:00:00Z_org-1", *encoded)
}
