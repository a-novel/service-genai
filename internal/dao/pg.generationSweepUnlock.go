package dao

import (
	"context"
	_ "embed"
	"errors"
	"fmt"

	"github.com/a-novel-kit/golib/otel"
)

//go:embed pg.generationSweepUnlock.sql
var generationSweepUnlockQuery string

// errSweepLockNotHeld is returned when the session no longer held the leader role it took.
var errSweepLockNotHeld = errors.New("the session held no sweep leader role")

// GenerationSweepUnlockRequest is the input to [PgGenerationSweepUnlock.Exec].
type GenerationSweepUnlockRequest struct {
	// Lock is the leader role to release, as [PgGenerationSweepLock] took it.
	Lock *GenerationSweepLock
}

// PgGenerationSweepUnlock releases the sweep's leader role, so the next sweep may take it.
//
// A session that fails to release the role is closed for good, which releases it too: the role never
// outlives the sweep that took it.
type PgGenerationSweepUnlock struct{}

// NewPgGenerationSweepUnlock returns a new PgGenerationSweepUnlock dao.
func NewPgGenerationSweepUnlock() *PgGenerationSweepUnlock {
	return &PgGenerationSweepUnlock{}
}

func (dao *PgGenerationSweepUnlock) Exec(ctx context.Context, request *GenerationSweepUnlockRequest) error {
	ctx, span := otel.Tracer().Start(ctx, "dao.PgGenerationSweepUnlock")
	defer span.End()

	conn := request.Lock.conn
	if conn == nil {
		return nil
	}

	var unlocked bool

	err := conn.NewRaw(generationSweepUnlockQuery, sweepLockClass, sweepLockObject).Scan(ctx, &unlocked)

	switch {
	case err != nil:
		err = errors.Join(fmt.Errorf("execute query: %w", err), discard(*conn))
	case !unlocked:
		err = errors.Join(errSweepLockNotHeld, discard(*conn))
	default:
		err = conn.Close()
	}

	if err != nil {
		return otel.ReportError(span, err)
	}

	return nil
}
