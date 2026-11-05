package dao

import (
	"context"
	_ "embed"
	"errors"
	"fmt"

	"github.com/uptrace/bun"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
)

//go:embed pg.generationSweepLock.sql
var generationSweepLockQuery string

// The sweep's leader role is an advisory lock in PostgreSQL's two-key form. That key space does not
// overlap the one-key space, where golib's migration lock and the submit's owner locks hash their
// keys, so no hash can collide with it.
const (
	// sweepLockClass is this service's class of advisory locks: "gena" in ASCII.
	sweepLockClass int32 = 0x67656e61
	// sweepLockObject is the sweep's leader role within the class.
	sweepLockObject int32 = 1
)

// ErrGenerationSweepLocked is returned by [PgGenerationSweepLock.Exec] when another session holds the
// sweep's leader role.
var ErrGenerationSweepLocked = errors.New("another sweep holds the leader role")

// errSweepLockInTransaction is returned when the context carries a transaction: its connection is
// shared, and a lock taken on it would outlive the sweep's use of it.
var errSweepLockInTransaction = errors.New("the sweep's leader role needs a connection pool, not a transaction")

// GenerationSweepLockRequest is the input to [PgGenerationSweepLock.Exec].
type GenerationSweepLockRequest struct{}

// GenerationSweepLock is the sweep's leader role, held by a connection of its own until
// [PgGenerationSweepUnlock] releases it.
type GenerationSweepLock struct {
	// conn is the session holding the lock. Nil holds nothing.
	conn *bun.Conn
}

// PgGenerationSweepLock takes the sweep's leader role, so one sweep runs at a time across every
// replica. It takes a connection out of the pool, which holds the role until released.
type PgGenerationSweepLock struct{}

// NewPgGenerationSweepLock returns a new PgGenerationSweepLock dao.
func NewPgGenerationSweepLock() *PgGenerationSweepLock {
	return &PgGenerationSweepLock{}
}

func (dao *PgGenerationSweepLock) Exec(
	ctx context.Context, _ *GenerationSweepLockRequest,
) (*GenerationSweepLock, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.PgGenerationSweepLock")
	defer span.End()

	db, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get postgres context: %w", err))
	}

	pool, ok := db.(*bun.DB)
	if !ok {
		return nil, otel.ReportError(span, errSweepLockInTransaction)
	}

	conn, err := pool.Conn(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("take a connection: %w", err))
	}

	var locked bool

	err = conn.NewRaw(generationSweepLockQuery, sweepLockClass, sweepLockObject).Scan(ctx, &locked)
	if err != nil {
		// The lock may have been granted with no answer to tell: the session ends with it.
		return nil, otel.ReportError(span, errors.Join(fmt.Errorf("execute query: %w", err), discard(conn)))
	}

	if !locked {
		return nil, otel.ReportError(span, errors.Join(ErrGenerationSweepLocked, conn.Close()))
	}

	return &GenerationSweepLock{conn: &conn}, nil
}
