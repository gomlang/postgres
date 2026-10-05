package adapter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

type Session interface{ session() }

func (*SessionState) session() {}

type SessionState struct {
	db                *database
	ctx               context.Context
	cancel            context.CancelFunc
	transaction, done bool
}
type database struct {
	mu         gate
	pool       *sql.DB
	conn       *sql.Conn
	ctx        context.Context
	cancel     context.CancelFunc
	closed     bool
	tx         *SessionState
	statements map[*StatementState]bool
	cursors    map[*CursorState]bool
}

func Open(dsn string, control Control) (Session, error) {
	if dsn == "" || !utf8.ValidString(dsn) || strings.IndexByte(dsn, 0) >= 0 {
		return nil, ErrArgument
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrArgument, err)
	}
	config.RuntimeParams["client_encoding"] = "UTF8"
	// Ad-hoc calls also use explicit PrepareContext, so a DSN cannot select a
	// simple-query path that permits multiple statements or interpolation.
	config.DefaultQueryExecMode = pgx.QueryExecModeDescribeExec
	pool := stdlib.OpenDB(*config)
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	root, cancel := context.WithCancel(context.Background())
	d := &database{mu: newGate(), pool: pool, ctx: root, cancel: cancel, statements: make(map[*StatementState]bool), cursors: make(map[*CursorState]bool)}
	ctx, finish, err := operation(root, control)
	if err != nil {
		cancel()
		pool.Close()
		return nil, err
	}
	defer finish()
	conn, err := pool.Conn(ctx)
	if err == nil {
		err = conn.PingContext(ctx)
	}
	if err != nil {
		if conn != nil {
			conn.Close()
		}
		cancel()
		pool.Close()
		return nil, cause(err, control, context.Background())
	}
	d.conn = conn
	return &SessionState{db: d, ctx: root, cancel: cancel}, nil
}
func (s *SessionState) check(exclusive bool) error {
	if s == nil || s.done || s.db.closed {
		return ErrClosed
	}
	if s.db.tx != nil && s.db.tx != s {
		return ErrBusy
	}
	if s.transaction && s.db.tx != s {
		return ErrClosed
	}
	if exclusive && len(s.db.cursors) > 0 {
		return ErrBusy
	}
	return nil
}
func IsClosed(handle Session) bool {
	s, ok := handle.(*SessionState)
	if !ok || s == nil {
		return true
	}
	s.db.mu.Lock()
	defer s.db.mu.Unlock()
	return s.done || s.db.closed
}
func (d *database) closeResources(owner *SessionState) error {
	var result error
	for c := range d.cursors {
		if owner == nil || c.owner == owner {
			result = errors.Join(result, c.close())
		}
	}
	for st := range d.statements {
		if owner == nil || st.owner == owner {
			result = errors.Join(result, st.close())
		}
	}
	return result
}
func (d *database) status() (byte, error) {
	var status byte
	err := d.conn.Raw(func(raw any) error {
		conn, ok := raw.(*stdlib.Conn)
		if !ok {
			return ErrType
		}
		if conn.Conn().IsClosed() {
			return ErrClosed
		}
		status = conn.Conn().PgConn().TxStatus()
		return nil
	})
	return status, err
}
func (d *database) endTx() {
	if d.tx != nil {
		d.tx.done = true
		d.tx.cancel()
		d.tx = nil
	}
}
func (d *database) recover() {
	status, err := d.status()
	if err != nil {
		d.closed = true
		d.cancel()
		d.closeResources(nil)
		d.endTx()
		d.conn.Close()
		d.pool.Close()
		return
	}
	if status == 'I' && d.tx != nil {
		d.closeResources(d.tx)
		d.endTx()
	}
}
func Close(handle Session) error {
	s, ok := handle.(*SessionState)
	if !ok || s == nil {
		return nil
	}
	if s.transaction {
		return Rollback(s)
	}
	d := s.db
	d.cancel()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	result := d.closeResources(nil)
	// The physical connection is discarded, not returned with an open transaction.
	if d.tx != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := d.conn.ExecContext(ctx, "ROLLBACK")
		cancel()
		result = errors.Join(result, err)
		d.endTx()
	}
	result = errors.Join(result, d.conn.Close(), d.pool.Close())
	return result
}
func Resources(handle Session) (int, int, int) {
	s, ok := handle.(*SessionState)
	if !ok || s == nil {
		return 0, 0, 0
	}
	d := s.db
	d.mu.Lock()
	defer d.mu.Unlock()
	tx := 0
	if d.tx != nil {
		tx = 1
	}
	return len(d.statements), len(d.cursors), tx
}

type Statement interface{ statement() }

func (*StatementState) statement() {}

type StatementState struct {
	owner  *SessionState
	stmt   *sql.Stmt
	ctx    context.Context
	cancel context.CancelFunc
	closed bool
}

func Prepare(handle Session, text string, control Control) (Statement, error) {
	s, ok := handle.(*SessionState)
	if !ok || s == nil {
		return nil, ErrClosed
	}
	if err := sqlText(text); err != nil {
		return nil, err
	}
	d := s.db
	if err := d.mu.lock(control, s.ctx); err != nil {
		return nil, err
	}
	defer d.mu.Unlock()
	if err := s.check(true); err != nil {
		return nil, err
	}
	ctx, finish, err := operation(s.ctx, control)
	if err != nil {
		return nil, err
	}
	defer finish()
	stmt, err := d.conn.PrepareContext(ctx, text)
	if err != nil {
		err = cause(err, control, s.ctx)
		d.recover()
		return nil, err
	}
	root, cancel := context.WithCancel(s.ctx)
	st := &StatementState{owner: s, stmt: stmt, ctx: root, cancel: cancel}
	d.statements[st] = true
	return st, nil
}
func (st *StatementState) close() error {
	if st.closed {
		return nil
	}
	st.closed = true
	delete(st.owner.db.statements, st)
	var result error
	for c := range st.owner.db.cursors {
		if c.statement == st {
			result = errors.Join(result, c.close())
		}
	}
	st.cancel()
	return errors.Join(result, st.stmt.Close())
}
func CloseStatement(handle Statement) error {
	st, ok := handle.(*StatementState)
	if !ok || st == nil {
		return nil
	}
	st.owner.db.mu.Lock()
	defer st.owner.db.mu.Unlock()
	err := st.close()
	st.owner.db.recover()
	return err
}
func Execute(handle Session, text string, values []Value, names []string, control Control) (int64, int64, error) {
	s, ok := handle.(*SessionState)
	if !ok || s == nil {
		return 0, 0, ErrClosed
	}
	if err := sqlText(text); err != nil {
		return 0, 0, err
	}
	args, err := parameters(values, names)
	if err != nil {
		return 0, 0, err
	}
	d := s.db
	if err = d.mu.lock(control, s.ctx); err != nil {
		return 0, 0, err
	}
	defer d.mu.Unlock()
	if err = s.check(true); err != nil {
		return 0, 0, err
	}
	ctx, finish, err := operation(s.ctx, control)
	if err != nil {
		return 0, 0, err
	}
	defer finish()
	stmt, err := d.conn.PrepareContext(ctx, text)
	if err != nil {
		err = cause(err, control, s.ctx)
		d.recover()
		return 0, 0, err
	}
	defer stmt.Close()
	result, err := stmt.ExecContext(ctx, args...)
	if err != nil {
		err = cause(err, control, s.ctx)
		d.recover()
		return 0, 0, err
	}
	n, err := result.RowsAffected()
	return n, 0, err
}
func ExecuteStatement(handle Statement, values []Value, names []string, control Control) (int64, int64, error) {
	st, ok := handle.(*StatementState)
	if !ok || st == nil {
		return 0, 0, ErrClosed
	}
	args, err := parameters(values, names)
	if err != nil {
		return 0, 0, err
	}
	s := st.owner
	d := s.db
	if err = d.mu.lock(control, st.ctx); err != nil {
		return 0, 0, err
	}
	defer d.mu.Unlock()
	if st.closed {
		return 0, 0, ErrClosed
	}
	if err = s.check(true); err != nil {
		return 0, 0, err
	}
	ctx, finish, err := operation(st.ctx, control)
	if err != nil {
		return 0, 0, err
	}
	defer finish()
	result, err := st.stmt.ExecContext(ctx, args...)
	if err != nil {
		err = cause(err, control, st.ctx)
		d.recover()
		return 0, 0, err
	}
	n, err := result.RowsAffected()
	return n, 0, err
}

type Cursor interface{ cursor() }

func (*CursorState) cursor() {}

type CursorState struct {
	owner             *SessionState
	statement         *StatementState
	rows              *sql.Rows
	temporary         *sql.Stmt
	columns           []Column
	finish            func()
	control           Control
	closed, exhausted bool
	failure           error
}

func newCursor(s *SessionState, st *StatementState, temporary *sql.Stmt, rows *sql.Rows, finish func(), control Control) (Cursor, error) {
	types, err := rows.ColumnTypes()
	if err != nil {
		rows.Close()
		if temporary != nil {
			temporary.Close()
		}
		finish()
		return nil, err
	}
	columns := make([]Column, len(types))
	for i, c := range types {
		columns[i] = Column{c.Name(), c.DatabaseTypeName()}
	}
	cursor := &CursorState{owner: s, statement: st, temporary: temporary, rows: rows, columns: columns, finish: finish, control: control}
	s.db.cursors[cursor] = true
	return cursor, nil
}
func Query(handle Session, text string, values []Value, names []string, control Control) (Cursor, error) {
	s, ok := handle.(*SessionState)
	if !ok || s == nil {
		return nil, ErrClosed
	}
	if err := sqlText(text); err != nil {
		return nil, err
	}
	args, err := parameters(values, names)
	if err != nil {
		return nil, err
	}
	d := s.db
	if err = d.mu.lock(control, s.ctx); err != nil {
		return nil, err
	}
	defer d.mu.Unlock()
	if err = s.check(true); err != nil {
		return nil, err
	}
	ctx, finish, err := operation(s.ctx, control)
	if err != nil {
		return nil, err
	}
	stmt, err := d.conn.PrepareContext(ctx, text)
	if err != nil {
		finish()
		err = cause(err, control, s.ctx)
		d.recover()
		return nil, err
	}
	rows, err := stmt.QueryContext(ctx, args...)
	if err != nil {
		stmt.Close()
		finish()
		err = cause(err, control, s.ctx)
		d.recover()
		return nil, err
	}
	return newCursor(s, nil, stmt, rows, finish, control)
}
func QueryStatement(handle Statement, values []Value, names []string, control Control) (Cursor, error) {
	st, ok := handle.(*StatementState)
	if !ok || st == nil {
		return nil, ErrClosed
	}
	args, err := parameters(values, names)
	if err != nil {
		return nil, err
	}
	s := st.owner
	d := s.db
	if err = d.mu.lock(control, st.ctx); err != nil {
		return nil, err
	}
	defer d.mu.Unlock()
	if st.closed {
		return nil, ErrClosed
	}
	if err = s.check(true); err != nil {
		return nil, err
	}
	ctx, finish, err := operation(st.ctx, control)
	if err != nil {
		return nil, err
	}
	rows, err := st.stmt.QueryContext(ctx, args...)
	if err != nil {
		finish()
		err = cause(err, control, st.ctx)
		d.recover()
		return nil, err
	}
	return newCursor(s, st, nil, rows, finish, control)
}
func (c *CursorState) close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	delete(c.owner.db.cursors, c)
	err := c.rows.Close()
	if c.temporary != nil {
		err = errors.Join(err, c.temporary.Close())
	}
	c.finish()
	return err
}
func CloseCursor(handle Cursor) error {
	c, ok := handle.(*CursorState)
	if !ok || c == nil {
		return nil
	}
	c.owner.db.mu.Lock()
	defer c.owner.db.mu.Unlock()
	err := c.close()
	c.owner.db.recover()
	return err
}
func CursorColumns(handle Cursor) []Column {
	c, ok := handle.(*CursorState)
	if !ok || c == nil {
		return nil
	}
	return append([]Column(nil), c.columns...)
}
func Next(handle Cursor) (bool, Row, error) {
	c, ok := handle.(*CursorState)
	if !ok || c == nil {
		return false, Row{}, ErrClosed
	}
	d := c.owner.db
	d.mu.Lock()
	defer d.mu.Unlock()
	if c.failure != nil {
		return false, Row{}, c.failure
	}
	if c.exhausted {
		return false, Row{}, nil
	}
	if c.closed || d.closed || c.owner.done {
		return false, Row{}, ErrClosed
	}
	fail := func(err error) (bool, Row, error) {
		c.failure = cause(err, c.control, c.owner.ctx)
		c.close()
		d.recover()
		return false, Row{}, c.failure
	}
	if control, ok := c.control.(*ControlState); ok && control != nil {
		if err := control.ctx.Err(); err != nil {
			return fail(err)
		}
	}
	if !c.rows.Next() {
		if err := c.rows.Err(); err != nil {
			return fail(err)
		}
		if c.rows.NextResultSet() {
			return fail(fmt.Errorf("%w: multiple result sets", ErrArgument))
		}
		if err := c.rows.Err(); err != nil {
			return fail(err)
		}
		c.exhausted = true
		return false, Row{}, c.close()
	}
	values := make([]any, len(c.columns))
	dest := make([]any, len(values))
	for i := range dest {
		dest[i] = &values[i]
	}
	if err := c.rows.Scan(dest...); err != nil {
		return fail(err)
	}
	row := Row{columns: append([]Column(nil), c.columns...), values: make([]Value, len(values))}
	for i, v := range values {
		converted, err := fromNative(v, c.columns[i].declared)
		if err != nil {
			return fail(err)
		}
		row.values[i] = converted
	}
	return true, row, nil
}
func Begin(handle Session, isolation int, readOnly bool, control Control) (Session, error) {
	s, ok := handle.(*SessionState)
	if !ok || s == nil {
		return nil, ErrClosed
	}
	if isolation < 0 || isolation > 2 {
		return nil, ErrArgument
	}
	d := s.db
	if err := d.mu.lock(control, s.ctx); err != nil {
		return nil, err
	}
	defer d.mu.Unlock()
	if err := s.check(true); err != nil {
		return nil, err
	}
	if s.transaction {
		return nil, ErrBusy
	}
	ctx, finish, err := operation(s.ctx, control)
	if err != nil {
		return nil, err
	}
	defer finish()
	text := "BEGIN ISOLATION LEVEL " + []string{"READ COMMITTED", "REPEATABLE READ", "SERIALIZABLE"}[isolation]
	if readOnly {
		text += " READ ONLY"
	} else {
		text += " READ WRITE"
	}
	if _, err = d.conn.ExecContext(ctx, text); err != nil {
		err = cause(err, control, s.ctx)
		d.recover()
		return nil, err
	}
	root, cancel := context.WithCancel(d.ctx)
	tx := &SessionState{db: d, ctx: root, cancel: cancel, transaction: true}
	d.tx = tx
	return tx, nil
}
func Commit(handle Session, control Control) error {
	s, ok := handle.(*SessionState)
	if !ok || s == nil || !s.transaction {
		return ErrArgument
	}
	d := s.db
	if err := d.mu.lock(control, d.ctx); err != nil {
		return err
	}
	defer d.mu.Unlock()
	if err := s.check(false); err != nil {
		return err
	}
	cleanup := d.closeResources(s)
	status, err := d.status()
	if err != nil {
		d.recover()
		return errors.Join(cleanup, err)
	}
	ctx, finish, err := operation(d.ctx, control)
	if err != nil {
		return errors.Join(cleanup, err)
	}
	defer finish()
	command := "COMMIT"
	if status == 'E' {
		command = "ROLLBACK"
	}
	_, err = d.conn.ExecContext(ctx, command)
	err = cause(err, control, d.ctx)
	if status == 'E' {
		err = errors.Join(ErrAborted, err)
	}
	d.recover()
	return errors.Join(cleanup, err)
}
func Rollback(handle Session) error {
	s, ok := handle.(*SessionState)
	if !ok || s == nil {
		return nil
	}
	if !s.transaction {
		return ErrArgument
	}
	s.cancel()
	d := s.db
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.done || d.closed {
		return nil
	}
	if d.tx != s {
		return ErrClosed
	}
	cleanup := d.closeResources(s)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := d.conn.ExecContext(ctx, "ROLLBACK")
	d.recover()
	// If cleanup cannot restore an idle server connection, discard it. Never let
	// uncertain transaction state escape to ordinary root execution.
	if d.tx == s {
		d.closed = true
		d.cancel()
		d.closeResources(nil)
		d.endTx()
		err = errors.Join(err, d.conn.Close(), d.pool.Close())
	}
	return errors.Join(cleanup, err)
}
