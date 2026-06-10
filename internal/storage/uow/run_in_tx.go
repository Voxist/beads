package uow

import (
	"context"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
)

const defaultRunInTxMaxElapsed = 15 * time.Second

// RunInTx opens a UnitOfWork, calls fn, and commits. The entire sequence is
// retried on transient serialization (1213/1205) and connection errors up to
// MaxElapsedTime. fn must be idempotent within a single transaction; the commit
// step is safe to replay because DOLT_COMMIT returns "nothing to commit" if a
// prior attempt already committed.
func RunInTx(ctx context.Context, p UnitOfWorkProvider, commitMsg string, fn func(uw UnitOfWork) error) error {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 25 * time.Millisecond
	bo.MaxElapsedTime = defaultRunInTxMaxElapsed

	return backoff.Retry(func() error {
		uw, err := p.NewUOW(ctx)
		if err != nil {
			if isSerializationError(err) || isInvalidConnectionError(err) {
				return err // retry
			}
			return backoff.Permanent(err)
		}
		defer uw.Close(ctx)

		if err := fn(uw); err != nil {
			return backoff.Permanent(err) // domain errors are not retryable
		}

		err = uw.Commit(ctx, commitMsg)
		if isSerializationError(err) || isInvalidConnectionError(err) {
			return err // retry the whole tx
		}
		if isNothingToCommit(err) {
			return nil // idempotent success
		}
		if err != nil {
			return backoff.Permanent(err)
		}
		return nil
	}, backoff.WithContext(bo, ctx))
}

// RunInTxMsg is like RunInTx but fn returns the commit message together with
// any error, allowing callers whose message depends on the domain result to
// compute it inside the same retry-safe closure.
func RunInTxMsg(ctx context.Context, p UnitOfWorkProvider, fn func(uw UnitOfWork) (string, error)) error {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 25 * time.Millisecond
	bo.MaxElapsedTime = defaultRunInTxMaxElapsed

	return backoff.Retry(func() error {
		uw, err := p.NewUOW(ctx)
		if err != nil {
			if isSerializationError(err) || isInvalidConnectionError(err) {
				return err
			}
			return backoff.Permanent(err)
		}
		defer uw.Close(ctx)

		msg, err := fn(uw)
		if err != nil {
			return backoff.Permanent(err)
		}

		err = uw.Commit(ctx, msg)
		if isSerializationError(err) || isInvalidConnectionError(err) {
			return err
		}
		if isNothingToCommit(err) {
			return nil
		}
		if err != nil {
			return backoff.Permanent(err)
		}
		return nil
	}, backoff.WithContext(bo, ctx))
}

func isNothingToCommit(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "nothing to commit") ||
		(strings.Contains(s, "no changes") && strings.Contains(s, "commit"))
}
