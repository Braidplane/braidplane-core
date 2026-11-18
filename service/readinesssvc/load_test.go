package readinesssvc

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
)

func TestPagesIncludesLastPage(t *testing.T) {
	cursor := "next-page"
	items, err := pages(func(req model.ListRequest) (model.ListResponse[string], error) {
		if req.Cursor == "" {
			return model.ListResponse[string]{Items: []string{"first"}, HasMore: true, NextCursor: &cursor}, nil
		}
		require.Equal(t, cursor, req.Cursor)
		return model.ListResponse[string]{Items: []string{"last"}}, nil
	})
	require.NoError(t, err)
	require.Equal(t, []string{"first", "last"}, items)
}
