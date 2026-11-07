package apierror

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	validatorpkg "github.com/Braidplane/braidplane-core/pkg/validate"
)

func TestMiddleware_APIError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		error          *Error
		expectedStatus int
		expectedCode   string
	}{
		{
			name:           "NotFound",
			error:          New(http.StatusNotFound, "USER_NOT_FOUND", "user not found"),
			expectedStatus: http.StatusNotFound,
			expectedCode:   "USER_NOT_FOUND",
		},
		{
			name:           "unauthorized",
			error:          Unauthorized(),
			expectedStatus: http.StatusUnauthorized,
			expectedCode:   CodeAuthUnauthorized,
		},
		{
			name:           "forbidden",
			error:          Forbidden("", ""),
			expectedStatus: http.StatusForbidden,
			expectedCode:   CodeAuthForbidden,
		},
		{
			name:           "ValidationFailed",
			error:          ValidationFailed("Request validation failed"),
			expectedStatus: http.StatusBadRequest,
			expectedCode:   CodeValidationFailed,
		},
		{
			name:           "InternalServerError",
			error:          InternalServerError(),
			expectedStatus: http.StatusInternalServerError,
			expectedCode:   CodeInternalServerError,
		},
		{
			name: "AlreadyExists",
			error: New(
				http.StatusConflict,
				"USER_EMAIL_ALREADY_EXISTS",
				"user email already exists",
			),
			expectedStatus: http.StatusConflict,
			expectedCode:   "USER_EMAIL_ALREADY_EXISTS",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app := fiber.New(fiber.Config{
				ErrorHandler: Middleware(),
			})

			app.Get("/test", func(fiber.Ctx) error {
				return tt.error
			})

			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", nil)
			resp, err := app.Test(req)
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, resp.Body.Close())
			})

			assert.Equal(t, tt.expectedStatus, resp.StatusCode)

			var errResp ErrorResponse
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			err = json.Unmarshal(body, &errResp)
			require.NoError(t, err)

			assert.Equal(t, tt.expectedCode, errResp.Error.Code)
			assert.NotEmpty(t, errResp.Error.Message)
			assert.Empty(t, errResp.Error.DetailedError)
			assert.NotContains(t, string(body), "\"detailed_error\"")
		})
	}
}

func TestMiddleware_APIErrorWithRequestID(t *testing.T) {
	t.Parallel()

	app := fiber.New(fiber.Config{
		ErrorHandler: Middleware(),
	})

	// Simulate requestid middleware
	app.Use(func(c fiber.Ctx) error {
		c.Locals("requestid", "test-request-id-123")
		return c.Next()
	})

	app.Get("/test", func(fiber.Ctx) error {
		return New(http.StatusNotFound, "USER_NOT_FOUND", "user not found")
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})

	var errResp ErrorResponse
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	err = json.Unmarshal(body, &errResp)
	require.NoError(t, err)

	assert.Equal(t, "test-request-id-123", errResp.Error.RequestID)
}

func TestMiddleware_UnknownErrorReturnsInternalServerError(t *testing.T) {
	t.Parallel()

	app := fiber.New(fiber.Config{
		ErrorHandler: Middleware(),
	})

	app.Get("/test", func(fiber.Ctx) error {
		return fmt.Errorf("database timeout while fetching users")
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})

	var errResp ErrorResponse
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	err = json.Unmarshal(body, &errResp)
	require.NoError(t, err)

	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	assert.Equal(t, CodeInternalServerError, errResp.Error.Code)
	assert.Empty(t, errResp.Error.DetailedError)
	assert.NotContains(t, string(body), "\"detailed_error\"")
}

func TestMiddleware_ValidationErrors(t *testing.T) {
	t.Parallel()

	// Create a validator instance
	v := validatorpkg.Default()

	type TestStruct struct {
		Email    string `validate:"required,email"`
		Password string `validate:"required,min=8"`
		Age      int    `validate:"min=18,max=100"`
		Role     string `validate:"oneof=admin user guest"`
	}

	tests := []struct {
		name               string
		input              TestStruct
		expectedFieldCount int
		expectedFields     map[string]string // field -> code
	}{
		{
			name: "MissingRequiredFields",
			input: TestStruct{
				Email:    "",
				Password: "",
				Age:      25,
				Role:     "user",
			},
			expectedFieldCount: 2,
			expectedFields: map[string]string{
				"email":    CodeValidationRequiredField,
				"password": CodeValidationRequiredField,
			},
		},
		{
			name: "InvalidEmailFormat",
			input: TestStruct{
				Email:    "not-an-email",
				Password: "validpassword",
				Age:      25,
				Role:     "user",
			},
			expectedFieldCount: 1,
			expectedFields: map[string]string{
				"email": CodeEmailInvalid,
			},
		},
		{
			name: "PasswordTooShort",
			input: TestStruct{
				Email:    "test@example.com",
				Password: "short",
				Age:      25,
				Role:     "user",
			},
			expectedFieldCount: 1,
			expectedFields: map[string]string{
				"password": CodeValueTooShort,
			},
		},
		{
			name: "AgeTooYoung",
			input: TestStruct{
				Email:    "test@example.com",
				Password: "validpassword",
				Age:      15,
				Role:     "user",
			},
			expectedFieldCount: 1,
			expectedFields: map[string]string{
				"age": CodeValueTooShort,
			},
		},
		{
			name: "AgeTooOld",
			input: TestStruct{
				Email:    "test@example.com",
				Password: "validpassword",
				Age:      150,
				Role:     "user",
			},
			expectedFieldCount: 1,
			expectedFields: map[string]string{
				"age": CodeValueTooLong,
			},
		},
		{
			name: "InvalidRole",
			input: TestStruct{
				Email:    "test@example.com",
				Password: "validpassword",
				Age:      25,
				Role:     "superadmin",
			},
			expectedFieldCount: 1,
			expectedFields: map[string]string{
				"role": CodeValueNotInList,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app := fiber.New(fiber.Config{
				ErrorHandler: Middleware(),
			})

			app.Post("/test", func(fiber.Ctx) error {
				err := v.Struct(tt.input)
				return err
			})

			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/test", nil)
			resp, err := app.Test(req)
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, resp.Body.Close())
			})

			assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

			var errResp ErrorResponse
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			err = json.Unmarshal(body, &errResp)
			require.NoError(t, err)

			assert.Equal(t, CodeValidationFailed, errResp.Error.Code)
			assert.Len(t, errResp.Error.ValidationErrors, tt.expectedFieldCount)
			assert.Empty(t, errResp.Error.DetailedError)

			// Check each expected field error
			for field, expectedCode := range tt.expectedFields {
				found := false
				for _, validationErr := range errResp.Error.ValidationErrors {
					if validationErr.Field == field {
						assert.Equal(t, expectedCode, validationErr.Code)
						assert.NotEmpty(t, validationErr.Message)
						found = true
						break
					}
				}
				assert.True(t, found, "Expected validation error for field %s not found", field)
			}
		})
	}
}

func TestMiddleware_FiberError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		fiberError     *fiber.Error
		expectedStatus int
		expectedCode   string
	}{
		{
			name:           "FiberBadRequest",
			fiberError:     fiber.NewError(fiber.StatusBadRequest, "invalid parameter"),
			expectedStatus: http.StatusBadRequest,
			expectedCode:   CodeValidationFailed,
		},
		{
			name:           "FiberUnauthorized",
			fiberError:     fiber.NewError(fiber.StatusUnauthorized, "unauthorized"),
			expectedStatus: http.StatusUnauthorized,
			expectedCode:   CodeAuthUnauthorized,
		},
		{
			name:           "FiberForbidden",
			fiberError:     fiber.NewError(fiber.StatusForbidden, "forbidden"),
			expectedStatus: http.StatusForbidden,
			expectedCode:   CodeAuthForbidden,
		},
		{
			name:           "FiberNotFound",
			fiberError:     fiber.NewError(fiber.StatusNotFound, "not found"),
			expectedStatus: http.StatusNotFound,
			expectedCode:   CodeInternalServerError,
		},
		{
			name:           "FiberMethodNotAllowed",
			fiberError:     fiber.NewError(fiber.StatusMethodNotAllowed, "method not allowed"),
			expectedStatus: http.StatusMethodNotAllowed,
			expectedCode:   CodeMethodNotAllowed,
		},
		{
			name:           "FiberInternalError",
			fiberError:     fiber.NewError(fiber.StatusInternalServerError, "something went wrong"),
			expectedStatus: http.StatusInternalServerError,
			expectedCode:   CodeInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app := fiber.New(fiber.Config{
				ErrorHandler: Middleware(),
			})

			app.Get("/test", func(fiber.Ctx) error {
				return tt.fiberError
			})

			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", nil)
			resp, err := app.Test(req)
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, resp.Body.Close())
			})

			assert.Equal(t, tt.expectedStatus, resp.StatusCode)

			var errResp ErrorResponse
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			err = json.Unmarshal(body, &errResp)
			require.NoError(t, err)

			assert.Equal(t, tt.expectedCode, errResp.Error.Code)
			assert.NotEmpty(t, errResp.Error.Message)
			assert.Empty(t, errResp.Error.DetailedError)
		})
	}
}

func TestMiddleware_UnknownError(t *testing.T) {
	t.Parallel()

	app := fiber.New(fiber.Config{
		ErrorHandler: Middleware(),
	})

	app.Get("/test", func(fiber.Ctx) error {
		return fmt.Errorf("some unexpected error")
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})

	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)

	var errResp ErrorResponse
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	err = json.Unmarshal(body, &errResp)
	require.NoError(t, err)

	assert.Equal(t, CodeInternalServerError, errResp.Error.Code)
	assert.NotEmpty(t, errResp.Error.Message)
	assert.Empty(t, errResp.Error.DetailedError)
}

func TestMiddleware_PreservesExplicitDetailedError(t *testing.T) {
	t.Parallel()

	app := fiber.New(fiber.Config{
		ErrorHandler: Middleware(),
	})

	app.Get("/test", func(fiber.Ctx) error {
		return ValidationFailed("Request validation failed").WithDetailedError(
			"invalid credentials format",
		)
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})

	var errResp ErrorResponse
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	err = json.Unmarshal(body, &errResp)
	require.NoError(t, err)

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, CodeValidationFailed, errResp.Error.Code)
	assert.Equal(t, "invalid credentials format", errResp.Error.DetailedError)
}

func TestMiddleware_SuccessfulRequest(t *testing.T) {
	t.Parallel()

	app := fiber.New(fiber.Config{
		ErrorHandler: Middleware(),
	})

	app.Get("/test", func(c fiber.Ctx) error {
		return c.JSON(map[string]string{"status": "ok"})
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var result map[string]string
	err = json.Unmarshal(body, &result)
	require.NoError(t, err)

	assert.Equal(t, "ok", result["status"])
}

func TestMiddleware_JSONUnmarshalTypeError(t *testing.T) {
	t.Parallel()

	app := fiber.New(fiber.Config{
		ErrorHandler: Middleware(),
	})

	type TestRequest struct {
		Age  int    `json:"age"`
		Name string `json:"name"`
	}

	app.Post("/test", func(c fiber.Ctx) error {
		var req TestRequest
		return c.Bind().Body(&req)
	})

	// Send string where int is expected
	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/test",
		strings.NewReader(`{"age":"not-a-number","name":"John"}`))

	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var errResp ErrorResponse
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	err = json.Unmarshal(body, &errResp)
	require.NoError(t, err)

	assert.Equal(t, CodeValidationFailed, errResp.Error.Code)
	assert.Equal(t, "Invalid value type in request body", errResp.Error.Message)
	require.Len(t, errResp.Error.ValidationErrors, 1)

	validationErr := errResp.Error.ValidationErrors[0]
	assert.Equal(t, "age", validationErr.Field)
	assert.Equal(t, CodeValidationInvalidFormat, validationErr.Code)
	assert.NotEmpty(t, validationErr.Message)
}

func TestMiddleware_JSONSyntaxError(t *testing.T) {
	t.Parallel()

	app := fiber.New(fiber.Config{
		ErrorHandler: Middleware(),
	})

	type TestRequest struct {
		Name string `json:"name"`
	}

	app.Post("/test", func(c fiber.Ctx) error {
		var req TestRequest
		return c.Bind().Body(&req)
	})

	// Send malformed JSON
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/test", strings.NewReader(`{"name": "John",}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var errResp ErrorResponse
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	err = json.Unmarshal(body, &errResp)
	require.NoError(t, err)

	assert.Equal(t, CodeValidationFailed, errResp.Error.Code)
	assert.Equal(t, "Malformed JSON in request body", errResp.Error.Message)
}
