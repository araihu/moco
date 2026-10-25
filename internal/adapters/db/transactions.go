package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/araihu/moco/internal/adapters/db/sqlc"
	"github.com/araihu/moco/internal/core/ports"
)

type transactionContextKey struct{}
type transactionScope struct {
	store         *Store
	tx            *sql.Tx
	nextSavepoint uint64
	afterCommit   []func(context.Context) error
}

var _ ports.TransactionRunner = (*Store)(nil)
var _ ports.CommitNotifier = (*Store)(nil)

// WithinTransaction is the outer request boundary. sql.DB remains immutable:
// other requests and the policy reloader never see an uncommitted connection.
func (s *Store) WithinTransaction(ctx context.Context, callback func(context.Context) error) error {
	if ctx.Value(transactionContextKey{}) != nil {
		return errors.New("an outer transaction is already active")
	}
	connection, err := s.database.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve audited connection: %w", err)
	}
	defer func() { _ = connection.Close() }()
	tx, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin audited transaction: %w", err)
	}
	commitFailed := false
	defer func() {
		rollbackErr := tx.Rollback()
		if commitFailed || ctx.Err() != nil || (rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone)) {
			// Some drivers leave a failed COMMIT's transaction open (e.g. a
			// deferred FK violation). Never return that connection to the pool.
			_ = connection.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	scope := &transactionScope{store: s, tx: tx}
	if err := callback(context.WithValue(ctx, transactionContextKey{}, scope)); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		commitFailed = true
		return fmt.Errorf("commit audited transaction: %w", err)
	}
	// Release the reserved connection before a notification tries to reload.
	_ = connection.Close()
	// Notifications see committed state even if the client has disconnected.
	notifyContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	var notificationErrors error
	for _, notify := range scope.afterCommit {
		notificationErrors = errors.Join(notificationErrors, notify(notifyContext))
	}
	if notificationErrors != nil {
		return &ports.PostCommitError{Err: notificationErrors}
	}
	return nil
}

func (s *Store) AfterCommit(ctx context.Context, notify func(context.Context) error) error {
	if scope, ok := ctx.Value(transactionContextKey{}).(*transactionScope); ok {
		if scope.store != s {
			return errors.New("cannot mix stores in an audited transaction")
		}
		scope.afterCommit = append(scope.afterCommit, notify)
		return nil
	}
	return notify(ctx)
}

// Existing repository operations keep their local atomicity using savepoints
// inside a request transaction. Failed or uncommitted inner operations roll back
// their own changes, and only the outer boundary can commit durable state.
type storeTransaction struct {
	*sql.Tx
	ctx       context.Context
	savepoint string
	finished  bool
}

func (s *Store) beginTransaction(ctx context.Context, options *sql.TxOptions) (*storeTransaction, error) {
	if scope, ok := ctx.Value(transactionContextKey{}).(*transactionScope); ok {
		if scope.store != s {
			return nil, errors.New("cannot mix stores in an audited transaction")
		}
		scope.nextSavepoint++
		name := "moco_" + strconv.FormatUint(scope.nextSavepoint, 10)
		if _, err := scope.tx.ExecContext(ctx, "SAVEPOINT "+name); err != nil {
			return nil, err
		} // #nosec G202 -- name is an internal counter, never request data.
		return &storeTransaction{Tx: scope.tx, ctx: ctx, savepoint: name}, nil
	}
	tx, err := s.database.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &storeTransaction{Tx: tx, ctx: ctx}, nil
}

func (t *storeTransaction) Commit() error {
	if t.savepoint == "" {
		return t.Tx.Commit()
	}
	if t.finished {
		return sql.ErrTxDone
	}
	_, err := t.ExecContext(t.ctx, "RELEASE SAVEPOINT "+t.savepoint) // #nosec G202 -- internally generated savepoint name.
	if err == nil {
		t.finished = true
	}
	return err
}

func (t *storeTransaction) Rollback() error {
	if t.savepoint == "" {
		return t.Tx.Rollback()
	}
	if t.finished {
		return sql.ErrTxDone
	}
	t.finished = true
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.ctx), time.Second)
	defer cancel()
	_, err := t.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+t.savepoint) // #nosec G202 -- internally generated savepoint name.
	if err != nil {
		return err
	}
	_, err = t.ExecContext(ctx, "RELEASE SAVEPOINT "+t.savepoint) // #nosec G202 -- internally generated savepoint name.
	return err
}

// contextDatabase routes sqlc queries and precondition reads to the same request
// transaction. It also fails closed for contexts originating from another store.
type contextDatabase struct{ store *Store }

func (d contextDatabase) executor(ctx context.Context) (sqlc.DBTX, context.Context) {
	if scope, ok := ctx.Value(transactionContextKey{}).(*transactionScope); ok {
		if scope.store == d.store {
			return scope.tx, ctx
		}
		// QueryRowContext cannot return an error directly. database/sql rejects a
		// canceled context before executing anything, including on the wrong DB.
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		return d.store.database, cancelled
	}
	return d.store.database, ctx
}

func (d contextDatabase) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	executor, ctx := d.executor(ctx)
	return executor.ExecContext(ctx, query, args...)
}
func (d contextDatabase) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	executor, ctx := d.executor(ctx)
	return executor.QueryContext(ctx, query, args...)
}
func (d contextDatabase) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	executor, ctx := d.executor(ctx)
	return executor.QueryRowContext(ctx, query, args...)
}
func (d contextDatabase) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	executor, ctx := d.executor(ctx)
	return executor.PrepareContext(ctx, query)
}
