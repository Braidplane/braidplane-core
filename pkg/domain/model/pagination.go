package model

const (
	defaultListLimit = 20
	maxListLimit     = 100

	ListOrderAsc  = "asc"
	ListOrderDesc = "desc"

	ListDirectionNext = "next"
	ListDirectionPrev = "prev"

	ListSortCreatedAt   = "created_at"
	ListSortUpdatedAt   = "updated_at"
	ListSortName        = "name"
	ListSortEmail       = "email"
	ListSortOrder       = "sort_order"
	ListSortExecutedAt  = "executed_at"
	ListSortOccurredAt  = "occurred_at"
	ListSortExpiresAt   = "expires_at"
	ListSortScheduledAt = "scheduled_at"
	ListSortLastSeenAt  = "last_seen_at"
)

// ListRequest is a shared pagination request type used by all list operations.
type ListRequest struct {
	Limit     int    // Maximum number of items to return
	Cursor    string // Cursor for pagination (optional, for fetching next page)
	SortBy    string // Sort field (endpoint-specific enum values)
	Order     string // Sort order: "asc" or "desc"
	Direction string // Page direction: "next" or "prev"
	Status    string // Optional endpoint-specific status filter
}

// ListResponse is a shared paginated response type used by all list operations.
type ListResponse[T any] struct {
	Items      []T     // Array of items in this page
	HasMore    bool    // Whether there are more results after this page
	NextCursor *string // Cursor for fetching the next page (nil if no more results)
	PrevCursor *string // Cursor for fetching the previous page (nil if not available)
}

func NormalizeListRequest(req ListRequest, defaultSort string) ListRequest {
	normalized := req

	if normalized.Limit <= 0 {
		normalized.Limit = defaultListLimit
	}
	if normalized.Limit > maxListLimit {
		normalized.Limit = maxListLimit
	}
	if normalized.SortBy == "" {
		normalized.SortBy = defaultSort
	}
	if normalized.Order == "" {
		normalized.Order = ListOrderDesc
	}
	if normalized.Order != ListOrderAsc && normalized.Order != ListOrderDesc {
		normalized.Order = ListOrderDesc
	}
	if normalized.Direction == "" {
		normalized.Direction = ListDirectionNext
	}
	if normalized.Direction != ListDirectionNext && normalized.Direction != ListDirectionPrev {
		normalized.Direction = ListDirectionNext
	}

	return normalized
}

func ReverseOrder(order string) string {
	if order == ListOrderAsc {
		return ListOrderDesc
	}
	return ListOrderAsc
}

func ReverseInPlace[T any](items []T) {
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
}

func PrepareListQuery(req ListRequest, defaultSort string) (ListRequest, ListRequest) {
	normalized := NormalizeListRequest(req, defaultSort)
	queryReq := normalized
	if normalized.Direction == ListDirectionPrev {
		queryReq.Order = ReverseOrder(normalized.Order)
	}
	queryReq.Limit = normalized.Limit + 1

	return normalized, queryReq
}

func FinalizePage[T any](items []T, normalized ListRequest) ([]T, bool) {
	hasMore := len(items) > normalized.Limit
	if hasMore {
		items = items[:normalized.Limit]
	}
	if normalized.Direction == ListDirectionPrev {
		ReverseInPlace(items)
	}

	return items, hasMore
}

func BuildBoundaryCursors[T any](
	items []T,
	normalized ListRequest,
	hasMore bool,
	cursorParts func(item T, sortBy string) (value, slug string),
) (*string, *string) {
	var nextCursor *string
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		cursorValue, cursorSlug := cursorParts(last, normalized.SortBy)
		nextCursor = EncodeCursor(cursorValue, cursorSlug)
	}

	var prevCursor *string
	if len(items) > 0 && (normalized.Cursor != "" || normalized.Direction == ListDirectionPrev) {
		first := items[0]
		cursorValue, cursorSlug := cursorParts(first, normalized.SortBy)
		prevCursor = EncodeCursor(cursorValue, cursorSlug)
	}

	return nextCursor, prevCursor
}

func EncodeCursor(value, slug string) *string {
	encoded := value + "_" + slug
	return &encoded
}
