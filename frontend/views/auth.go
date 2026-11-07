package views

import "net/url"

// AuthReturnURL appends a safe return target to links between authentication pages.
func AuthReturnURL(path, returnTo string) string {
	if returnTo == "" {
		return path
	}

	values := url.Values{}
	values.Set("return_to", returnTo)
	return path + "?" + values.Encode()
}

// WorkspaceGateUsageURL switches gate intent while preserving the safe continuation target.
func WorkspaceGateUsageURL(usageType, returnTo string) string {
	values := url.Values{}
	values.Set("usage_type", usageType)
	if returnTo != "" {
		values.Set("return_to", returnTo)
	}
	return "/workspace?" + values.Encode()
}
