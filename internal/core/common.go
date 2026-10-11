package core

import (
	"errors"
	"strings"

	"github.com/go-playground/validator/v10"

	"github.com/a-novel/service-genai/internal/dao"
)

// ErrInvalidRequest is returned by a service Exec when its request fails validation.
var ErrInvalidRequest = errors.New("invalid request")

// ErrGenerationNotFound is returned when the owner has no such generation. A generation owned by
// somebody else reports this too, so an identifier cannot be probed for existence.
var ErrGenerationNotFound = dao.ErrGenerationGetNotFound

// ErrUsageCapReached is returned when the user already reached a limit the submit answers to. Its
// message names the limit.
var ErrUsageCapReached = errors.New("usage cap reached")

var validate = validator.New(validator.WithRequiredStructEnabled())

func init() {
	err := validate.RegisterValidation("notblank", validateNotBlank)
	if err != nil {
		panic(err)
	}
}

// validateNotBlank refuses a string with no non-whitespace character. Register it with the
// "notblank" struct tag.
func validateNotBlank(fl validator.FieldLevel) bool {
	return strings.TrimSpace(fl.Field().String()) != ""
}
