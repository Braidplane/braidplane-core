package views

import "github.com/a-h/templ"

// FormFieldError associates a server validation error with its control.
func FormFieldError(invalid bool, feedbackID string) templ.Attributes {
	if !invalid {
		return templ.Attributes{}
	}
	return templ.Attributes{
		"aria-invalid":          "true",
		"aria-describedby":      feedbackID,
		"aria-errormessage":     feedbackID,
		"autofocus":             true,
		"data-form-error-focus": true,
	}
}
