package adapter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

var ErrClosed = errors.New("postgres resource is closed")
var ErrBusy = errors.New("postgres connection has an active cursor or transaction")
var ErrArgument = errors.New("invalid postgres argument")
var ErrType = errors.New("unsupported postgres value")
var ErrAborted = errors.New("postgres transaction was aborted; changes were rolled back")

type Control interface{ control() }
type ControlState struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func (*ControlState) control() {}
func NewControl(milliseconds int64) (Control, error) {
	if milliseconds < 0 || milliseconds > math.MaxInt64/int64(time.Millisecond) {
		return nil, ErrArgument
	}
	if milliseconds == 0 {
		ctx, cancel := context.WithCancel(context.Background())
		return &ControlState{ctx, cancel}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(milliseconds)*time.Millisecond)
	return &ControlState{ctx, cancel}, nil
}
func Cancel(value Control) {
	if c, ok := value.(*ControlState); ok && c != nil {
		c.cancel()
	}
}
func operation(root context.Context, value Control) (context.Context, func(), error) {
	c, ok := value.(*ControlState)
	if !ok || c == nil {
		return nil, nil, ErrArgument
	}
	if err := c.ctx.Err(); err != nil {
		return nil, nil, err
	}
	if root.Err() != nil {
		return nil, nil, ErrClosed
	}
	ctx, cancel := context.WithCancel(root)
	stop := context.AfterFunc(c.ctx, cancel)
	if c.ctx.Err() != nil {
		cancel()
	}
	return ctx, func() { stop(); cancel() }, nil
}
func cause(err error, control Control, root context.Context) error {
	if err == nil {
		return nil
	}
	if c, ok := control.(*ControlState); ok && c != nil && c.ctx.Err() != nil {
		return fmt.Errorf("%w: %w", c.ctx.Err(), err)
	}
	if root.Err() != nil {
		return fmt.Errorf("%w: %w", ErrClosed, err)
	}
	return err
}

type gate chan struct{}

func newGate() gate    { g := make(gate, 1); g <- struct{}{}; return g }
func (g gate) Lock()   { <-g }
func (g gate) Unlock() { g <- struct{}{} }
func (g gate) lock(value Control, root context.Context) error {
	c, ok := value.(*ControlState)
	if !ok || c == nil {
		return ErrArgument
	}
	if err := c.ctx.Err(); err != nil {
		return err
	}
	select {
	case <-g:
		if err := c.ctx.Err(); err != nil {
			g.Unlock()
			return err
		}
		if root.Err() != nil {
			g.Unlock()
			return ErrClosed
		}
		return nil
	case <-c.ctx.Done():
		return c.ctx.Err()
	case <-root.Done():
		return ErrClosed
	}
}
func ErrorClass(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrClosed), errors.Is(err, sql.ErrConnDone), errors.Is(err, sql.ErrTxDone):
		return 1
	case errors.Is(err, ErrBusy):
		return 2
	case errors.Is(err, ErrArgument):
		return 3
	case errors.Is(err, ErrType):
		return 4
	case errors.Is(err, context.DeadlineExceeded):
		return 5
	case errors.Is(err, context.Canceled):
		return 6
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "55P03" {
		return 2
	}
	return 7
}
func ErrorState(err error) string {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code
	}
	return ""
}
