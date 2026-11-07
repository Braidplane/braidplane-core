package views

import (
	"context"
	"net/url"
)

type entityPageURLKey struct{}

// WithEntityPageURL supplies request navigation only; entity loaders and permissions stay with handlers.
func WithEntityPageURL(ctx context.Context, rawURL string) context.Context {
	return context.WithValue(ctx, entityPageURLKey{}, rawURL)
}

func entityPageURL(ctx context.Context) *url.URL {
	raw, _ := ctx.Value(entityPageURLKey{}).(string)
	u, err := url.Parse(raw)
	if err != nil {
		return &url.URL{}
	}
	return u
}

// entityListHref retains only inventory state on the exact scoped list route.
func entityListHref(ctx context.Context, fallback, selectionKey, selected string) string {
	u, _ := url.Parse(fallback)
	current := entityPageURL(ctx)
	if current.Path == u.Path {
		values := u.Query()
		for _, key := range []string{"q", "sync_status", "draft", "deployment", "run_status", "filter", "cursor", "direction", "ds", "deleted", "status", "pipeline_slug", "mode", "backfill_series_slug"} {
			if value := current.Query().Get(key); value != "" {
				values.Set(key, value)
			}
		}
		u.RawQuery = values.Encode()
	}
	values := u.Query()
	values.Del(selectionKey)
	if selectionKey == "run_slug" || selectionKey == "trigger_slug" {
		values.Del("preview")
		if selected == "" {
			values.Set("preview", "closed")
		}
	}
	if selected != "" {
		values.Set(selectionKey, selected)
	}
	u.RawQuery = values.Encode()
	return u.String()
}

func entityDetailHref(detailHref, listHref string) string {
	u, _ := url.Parse(detailHref)
	values := u.Query()
	values.Set("list_return", listHref)
	u.RawQuery = values.Encode()
	return u.String()
}

func entityBackHref(ctx context.Context, fallback string) string {
	candidate, err := url.Parse(entityPageURL(ctx).Query().Get("list_return"))
	expected, _ := url.Parse(fallback)
	// Exact path matching prevents cross-tenant and cross-resource return links.
	if err != nil || candidate.IsAbs() || candidate.Host != "" || candidate.User != nil || candidate.Path != expected.Path || candidate.Fragment != "" {
		return fallback
	}
	return candidate.String()
}

func entitySectionHref(ctx context.Context, detailHref, section string) string {
	u, _ := url.Parse(detailHref)
	values := u.Query()
	values.Set("section", section)
	if listReturn := entityPageURL(ctx).Query().Get("list_return"); listReturn != "" {
		values.Set("list_return", listReturn)
	}
	u.RawQuery = values.Encode()
	return u.String()
}
