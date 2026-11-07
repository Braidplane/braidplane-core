package views

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDestructiveConfirmationFormatsEntityContextAndImpact verifies the shared destructive copy shape.
func TestDestructiveConfirmationFormatsEntityContextAndImpact(t *testing.T) {
	t.Parallel()

	require.Equal(
		t,
		`Delete data source "Production DB (prod-db)"? This cannot be undone.`,
		DestructiveConfirmation("Delete", "data source", DestructiveResourceLabel("Production DB", "prod-db"), "This cannot be undone"),
	)
	require.Equal(
		t,
		`Remove this member? They will lose access immediately.`,
		DestructiveConfirmation("Remove", "member", "", "They will lose access immediately."),
	)
}

// TestDestructiveTemplateConfirmationsUseSharedHelper keeps simple confirmations on the shared helper/fallback path.
func TestDestructiveTemplateConfirmationsUseSharedHelper(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("*.templ")
	require.NoError(t, err)
	require.NotEmpty(t, files)

	for _, file := range files {
		contentBytes, readErr := os.ReadFile(file)
		require.NoError(t, readErr)
		content := string(contentBytes)

		require.NotContains(t, content, `hx-confirm="`, "%s uses inline destructive confirmation copy", file)
		for _, offset := range allStringOffsets(content, "hx-confirm={") {
			formTag := formTagAtOffset(content, offset)
			require.NotEmpty(t, formTag, "%s places hx-confirm somewhere other than the HTMX request form", file)
			require.Contains(t, formTag, "DestructiveConfirmation(", "%s has hx-confirm without the shared helper", file)
			require.Contains(t, formTag, "data-destructive-confirm={", "%s has hx-confirm without the non-HTMX fallback", file)
		}
		for _, offset := range allStringOffsets(content, "data-destructive-confirm={") {
			formTag := formTagAtOffset(content, offset)
			require.NotEmpty(t, formTag, "%s places fallback confirmation somewhere other than the request form", file)
			require.Contains(t, formTag, "DestructiveConfirmation(", "%s has fallback confirmation without the shared helper", file)
		}
		for _, offset := range allStringOffsets(content, "<form") {
			formTag := formTagAtOffset(content, offset)
			if formTag == "" || !simpleDestructiveFormTag(formTag) || formInsideProtectedActionModal(content, offset) {
				continue
			}
			require.Contains(t, formTag, "data-destructive-confirm={", "%s has a destructive form without the non-HTMX fallback", file)
			require.Contains(t, formTag, "DestructiveConfirmation(", "%s has a destructive form without the shared helper", file)
		}
	}
}

// TestFormTagAtOffsetIncludesFormStart keeps form scans from skipping exact tag offsets.
func TestFormTagAtOffsetIncludesFormStart(t *testing.T) {
	t.Parallel()

	content := `<section><form method="post" action="/things/delete" data-destructive-confirm={ DestructiveConfirmation("Delete", "thing", "example", "This cannot be undone.") }></form></section>`
	offset := strings.Index(content, "<form")
	require.NotEqual(t, -1, offset)

	require.Equal(
		t,
		`<form method="post" action="/things/delete" data-destructive-confirm={ DestructiveConfirmation("Delete", "thing", "example", "This cannot be undone.") }>`,
		formTagAtOffset(content, offset),
	)
}

// formTagAtOffset returns the surrounding opening form tag when offset is inside one.
func formTagAtOffset(content string, offset int) string {
	if offset < 0 || offset >= len(content) {
		return ""
	}
	tagStart := offset
	if !strings.HasPrefix(content[tagStart:], "<form") {
		tagStart = strings.LastIndex(content[:offset], "<")
	}
	if tagStart == -1 {
		return ""
	}
	if !strings.HasPrefix(content[tagStart:], "<form") {
		return ""
	}
	tagEnd := strings.Index(content[offset:], ">")
	if tagEnd == -1 {
		return ""
	}
	return content[tagStart : offset+tagEnd+1]
}

// simpleDestructiveFormTag reports whether a non-modal form action uses a destructive route.
func simpleDestructiveFormTag(formTag string) bool {
	if !strings.Contains(formTag, `method="post"`) {
		return false
	}
	for _, marker := range []string{"/delete", "/remove", "/purge", "/revoke", "/rotate", "/disable", "/decline", "draft/delete"} {
		if strings.Contains(formTag, marker) {
			return true
		}
	}
	return false
}

// formInsideProtectedActionModal reports whether the form belongs to an existing typed confirmation modal.
func formInsideProtectedActionModal(content string, offset int) bool {
	lastDialogStart := strings.LastIndex(content[:offset], "<dialog")
	if lastDialogStart == -1 {
		return false
	}
	lastDialogEnd := strings.LastIndex(content[:offset], "</dialog>")
	if lastDialogEnd > lastDialogStart {
		return false
	}
	return strings.Contains(content[lastDialogStart:offset], "data-protected-action-modal")
}

// allStringOffsets returns every byte offset where needle starts in haystack.
func allStringOffsets(haystack, needle string) []int {
	var offsets []int
	searchStart := 0
	for {
		index := strings.Index(haystack[searchStart:], needle)
		if index == -1 {
			return offsets
		}
		offset := searchStart + index
		offsets = append(offsets, offset)
		searchStart = offset + len(needle)
	}
}
