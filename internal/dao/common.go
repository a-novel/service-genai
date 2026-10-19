package dao

import (
	"errors"
	"strings"

	"github.com/uptrace/bun/driver/pgdriver"
)

// ErrGenerationChanged is returned when a transition's preconditions no longer hold: another check
// committed first, or the generation moved to a state the transition does not apply to. The caller
// re-reads rather than retrying the write.
var ErrGenerationChanged = errors.New("generation changed before the transition")

// ErrRefused is returned when the database refuses the values a statement writes: a value its column
// cannot hold, a constraint it breaks, a limit it exceeds. The same values fail the same way however
// often they are sent, unlike a lock wait or a lost connection.
var ErrRefused = errors.New("values refused by the database")

// refusedClasses are the SQLSTATE classes of an error raised for a statement's values: a data
// exception, an integrity constraint violation and an exceeded limit.
var refusedClasses = []string{"22", "23", "54"}

// refused joins [ErrRefused] to an error the database raised for a statement's values.
func refused(err error) error {
	var pgErr pgdriver.Error
	if !errors.As(err, &pgErr) {
		return err
	}

	for _, class := range refusedClasses {
		if strings.HasPrefix(pgErr.Field('C'), class) {
			return errors.Join(err, ErrRefused)
		}
	}

	return err
}
