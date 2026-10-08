package dao

import "errors"

// ErrGenerationChanged is returned when a transition's preconditions no longer hold: another check
// committed first, or the generation moved to a state the transition does not apply to. The caller
// re-reads rather than retrying the write.
var ErrGenerationChanged = errors.New("generation changed before the transition")
