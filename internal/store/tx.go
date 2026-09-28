package store

import (
	"context"
	"fmt"

	"fleetdeck/internal/store/sqlcgen"
)

// WithTx runs fn inside a Postgres transaction. The *Queries handed to fn is
// bound to that transaction, so every method called through it participates in
// the same atomic unit. If fn returns an error the transaction is rolled back;
// on success it is committed.
//
// A panic inside fn rolls the transaction back and re-panics.
func (s *Store) WithTx(ctx context.Context, fn func(*Queries) error) (err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin tx: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()

	if err = fn(newQueries(sqlcgen.New(tx))); err != nil {
		return err
	}

	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit tx: %w", err)
	}
	return nil
}
