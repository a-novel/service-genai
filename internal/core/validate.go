package core

import (
	"errors"

	"github.com/go-playground/validator/v10"
	"github.com/go-playground/validator/v10/non-standard/validators"
)

var validate = validator.New(validator.WithRequiredStructEnabled())

// ErrInvalidRequest is returned by a service Exec when its request fails struct validation.
var ErrInvalidRequest = errors.New("invalid request")

// The "notblank" tag rejects a string that is empty or holds only whitespace and the ASCII
// separators U+001C to U+001F.
func init() {
	err := validate.RegisterValidation("notblank", validators.NotBlank)
	if err != nil {
		panic(err)
	}
}
