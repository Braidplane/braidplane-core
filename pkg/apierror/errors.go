package apierror

// Error represents a structured API error
type Error struct {
	Code             string            `json:"code"`
	Message          string            `json:"message"`
	DetailedError    string            `json:"detailed_error,omitempty"`
	ValidationErrors []ValidationError `json:"validation_errors,omitempty"`
	RequestID        string            `json:"request_id,omitempty"`

	// Internal fields (not serialized)
	HTTPStatus int `json:"-"`
}

// ValidationError represents a field-level validation error
type ValidationError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ErrorResponse wraps the error for API responses
type ErrorResponse struct {
	Error *Error `json:"error"`
}

// Error implements the error interface
func (e *Error) Error() string {
	return e.Message
}

// WithDetailedError adds backend diagnostic details for disclosure UIs.
func (e *Error) WithDetailedError(detailedError string) *Error {
	e.DetailedError = detailedError
	return e
}

// withMessage overrides the error message
func (e *Error) withMessage(message string) *Error {
	e.Message = message
	return e
}

// withValidationErrors adds field-level validation errors
func (e *Error) withValidationErrors(errors []ValidationError) *Error {
	e.ValidationErrors = errors
	return e
}

// withRequestID adds request ID for tracing
func (e *Error) withRequestID(requestID string) *Error {
	e.RequestID = requestID
	return e
}
