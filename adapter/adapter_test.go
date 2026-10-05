package adapter

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func control(t *testing.T) Control {
	t.Helper()
	c, err := NewControl(0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Cancel(c) })
	return c
}
func connect(t *testing.T) Session {
	t.Helper()
	dsn := os.Getenv("GOML_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Fatal("GOML_POSTGRES_TEST_DSN is required: use scripts/with-postgres.py")
	}
	s, err := Open(dsn, control(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Close(s) })
	return s
}
func exec(t *testing.T, s Session, text string, args ...Value) {
	t.Helper()
	_, id, err := Execute(s, text, args, nil, control(t))
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	if id != 0 {
		t.Fatalf("unsupported insert id %d", id)
	}
}
func rows(t *testing.T, s Session, text string, args ...Value) []Row {
	t.Helper()
	c, err := Query(s, text, args, nil, control(t))
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	defer CloseCursor(c)
	var result []Row
	for {
		ok, row, err := Next(c)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		result = append(result, row)
	}
	if ok, _, err := Next(c); ok || err != nil {
		t.Fatalf("EOF %v %v", ok, err)
	}
	return result
}
func requireClass(t *testing.T, err error, class int) {
	t.Helper()
	if err == nil || ErrorClass(err) != class {
		t.Fatalf("class %d: %v (actual %d)", class, err, ErrorClass(err))
	}
}
func requireState(t *testing.T, err error, state string) {
	t.Helper()
	if err == nil || ErrorState(err) != state {
		t.Fatalf("SQLSTATE %s: %v (%s)", state, err, ErrorState(err))
	}
}
func TestErrorChainsPreserveBackendCause(t *testing.T) {
	backend := &pgconn.PgError{Code: "57014", Message: "canceling statement due to user request"}
	for _, scenario := range []string{"cancel", "deadline", "close"} {
		t.Run(scenario, func(t *testing.T) {
			c := control(t)
			root := context.Background()
			var expected error = context.Canceled
			class := 6
			switch scenario {
			case "cancel":
				Cancel(c)
			case "deadline":
				ctx, cancel := context.WithDeadline(root, time.Unix(0, 0))
				defer cancel()
				c = &ControlState{ctx: ctx, cancel: cancel}
				expected, class = context.DeadlineExceeded, 5
			case "close":
				ctx, cancel := context.WithCancel(root)
				cancel()
				root = ctx
				expected, class = ErrClosed, 1
			}
			err := FailureError(failure(cause(backend, c, root)))
			var pg *pgconn.PgError
			if !errors.Is(err, expected) || !errors.As(err, &pg) || pg != backend || ErrorClass(err) != class || ErrorState(err) != "57014" {
				t.Fatalf("lost classification or backend cause: %v, class=%d, SQLSTATE=%s", err, ErrorClass(err), ErrorState(err))
			}
			if scenario != "close" {
				both := FailureError(failure(cause(sql.ErrConnDone, c, root)))
				if ErrorClass(both) != 1 || !errors.Is(both, sql.ErrConnDone) || !errors.Is(both, expected) {
					t.Fatalf("closed must take precedence while preserving both causes: %v", both)
				}
			}
		})
	}
	_, err := Open("postgresql://%zz", control(t))
	var parse *pgconn.ParseConfigError
	if !errors.Is(err, ErrArgument) || !errors.As(err, &parse) {
		t.Fatalf("lost DSN parse cause: %v", err)
	}
}
func TestValuesBindingsAndOwnership(t *testing.T) {
	s := connect(t)
	exec(t, s, "CREATE TEMP TABLE values_test(id bigint PRIMARY KEY, body bytea, name text)")
	blob := []byte{0, 255, 13, 10, 128}
	bound := Blob(blob)
	blob[0] = 99
	text := "é😀'); DROP TABLE values_test; --"
	exec(t, s, "INSERT INTO values_test VALUES($1,$2,$3)", Integer(7), bound, Text(text))
	result := rows(t, s, "SELECT id,body,name FROM values_test WHERE id=$1", Integer(7))
	if len(result) != 1 {
		t.Fatal(result)
	}
	v := RowValues(result[0])
	if ValueInteger(v[0]) != 7 || !bytes.Equal(ValueBlob(v[1]), []byte{0, 255, 13, 10, 128}) || ValueText(v[2]) != text {
		t.Fatal(v)
	}
	data := ValueBlob(v[1])
	data[0] = 42
	if ValueBlob(RowValues(result[0])[1])[0] != 0 {
		t.Fatal("row alias")
	}
	r := rows(t, s, "SELECT $1::bigint,$2::double precision,$3::text,$4::bytea,$5::text,true,false,12345678901234567890.25::numeric,'{\"x\":1}'::jsonb,'00000000-0000-0000-0000-000000000001'::uuid,DATE '2024-02-29',TIMESTAMP '2024-02-29 12:34:56.25',TIMESTAMPTZ '2024-02-29 12:34:56+03',ARRAY[1,2]", Integer(-9223372036854775808), Real(1.25), Text(""), Blob(nil), Null())[0]
	vs := RowValues(r)
	if ValueInteger(vs[0]) != -9223372036854775808 || ValueReal(vs[1]) != 1.25 || ValueKind(vs[2]) != 3 || ValueText(vs[2]) != "" || ValueKind(vs[3]) != 4 || len(ValueBlob(vs[3])) != 0 || ValueKind(vs[4]) != 0 || ValueInteger(vs[5]) != 1 || ValueInteger(vs[6]) != 0 {
		t.Fatalf("scalar %v", vs)
	}
	expected := []string{"12345678901234567890.25", "{\"x\": 1}", "00000000-0000-0000-0000-000000000001", "2024-02-29", "2024-02-29T12:34:56.25", "2024-02-29T09:34:56Z", "{1,2}"}
	for i, want := range expected {
		if got := ValueText(vs[i+7]); got != want {
			t.Fatalf("column %d: %q want %q kind%d", i+7, got, want, ValueKind(vs[i+7]))
		}
	}
	for _, invalid := range []Value{Text("x\x00y"), Text(string([]byte{255}))} {
		_, _, err := Execute(s, "SELECT $1::text", []Value{invalid}, nil, control(t))
		requireClass(t, err, 4)
	}
	_, _, err := Execute(s, "SELECT $1", []Value{Integer(1)}, []string{"x"}, control(t))
	requireClass(t, err, 3)
	if _, _, err = Execute(s, "SELECT $1::bigint", nil, nil, control(t)); err == nil {
		t.Fatal("missing binding accepted")
	}
	rows(t, s, "SELECT 1")
}
func TestStatementsCursorClosureAndBusy(t *testing.T) {
	s := connect(t)
	st, err := Prepare(s, "SELECT $1::bigint AS value UNION ALL SELECT 9", control(t))
	if err != nil {
		t.Fatal(err)
	}
	stream, err := Query(s, "SELECT i,pg_sleep(0.001) FROM generate_series(1,1000) i", nil, nil, control(t))
	if err != nil {
		t.Fatal(err)
	}
	if err = CloseCursor(stream); err != nil {
		t.Fatal("normal early close", err)
	}
	rows(t, s, "SELECT 1")
	for n := 0; n < 5; n++ {
		c, err := QueryStatement(st, []Value{Integer(int64(n))}, nil, control(t))
		if err != nil {
			t.Fatal(err)
		}
		if got := CursorColumns(c); len(got) != 1 || ColumnName(got[0]) != "value" || ColumnType(got[0]) != "INT8" {
			t.Fatal(got)
		}
		_, _, err = Execute(s, "SELECT 2", nil, nil, control(t))
		requireClass(t, err, 2)
		_, err = Prepare(s, "SELECT 2", control(t))
		requireClass(t, err, 2)
		ok, row, err := Next(c)
		if !ok || err != nil || ValueInteger(RowValues(row)[0]) != int64(n) {
			t.Fatal(ok, row, err)
		}
		if err = CloseCursor(c); err != nil {
			t.Fatal(err)
		}
		if err = CloseCursor(c); err != nil {
			t.Fatal(err)
		}
		_, _, err = Next(c)
		requireClass(t, err, 1)
	}
	c, err := QueryStatement(st, []Value{Integer(3)}, nil, control(t))
	if err != nil {
		t.Fatal(err)
	}
	if err = CloseStatement(st); err != nil {
		t.Fatal(err)
	}
	_, _, err = Next(c)
	requireClass(t, err, 1)
	if err = CloseStatement(st); err != nil {
		t.Fatal(err)
	}
	_, _, err = ExecuteStatement(st, nil, nil, control(t))
	requireClass(t, err, 1)
	a, b, d := Resources(s)
	if a != 0 || b != 0 || d != 0 {
		t.Fatal(a, b, d)
	}
	rows(t, s, "SELECT 1")
	if err = Close(s); err != nil {
		t.Fatal(err)
	}
	if err = Close(s); err != nil {
		t.Fatal(err)
	}
	_, _, err = Execute(s, "SELECT 1", nil, nil, control(t))
	requireClass(t, err, 1)
}
func TestManagedTransactionsAndRecovery(t *testing.T) {
	s := connect(t)
	exec(t, s, "CREATE TEMP TABLE transaction_test(id integer PRIMARY KEY)")
	c := control(t)
	tx, err := Begin(s, 0, false, c)
	if err != nil {
		t.Fatal(err)
	}
	Cancel(c)
	exec(t, tx, "INSERT INTO transaction_test VALUES(1)")
	_, _, err = Execute(s, "INSERT INTO transaction_test VALUES(99)", nil, nil, control(t))
	requireClass(t, err, 2)
	st, err := Prepare(tx, "SELECT id FROM transaction_test", control(t))
	if err != nil {
		t.Fatal(err)
	}
	if err = Commit(tx, control(t)); err != nil {
		t.Fatal(err)
	}
	_, _, err = ExecuteStatement(st, nil, nil, control(t))
	requireClass(t, err, 1)
	requireClass(t, Commit(tx, control(t)), 1)
	if err = Rollback(tx); err != nil {
		t.Fatal(err)
	}
	if got := ValueInteger(RowValues(rows(t, s, "SELECT count(*) FROM transaction_test")[0])[0]); got != 1 {
		t.Fatal(got)
	}
	_, _, err = Execute(s, "INSERT INTO transaction_test VALUES(1)", nil, nil, control(t))
	requireState(t, err, "23505")
	var pg *pgconn.PgError
	if !errors.As(FailureError(failure(err)), &pg) {
		t.Fatal("native cause lost")
	}
	rows(t, s, "SELECT 1")
	tx, err = Begin(s, 0, false, control(t))
	if err != nil {
		t.Fatal(err)
	}
	exec(t, tx, "INSERT INTO transaction_test VALUES(2)")
	_, _, err = Execute(tx, "INSERT INTO transaction_test VALUES(1)", nil, nil, control(t))
	requireState(t, err, "23505")
	_, _, err = Execute(tx, "SELECT 1", nil, nil, control(t))
	requireState(t, err, "25P02")
	if err = Commit(tx, control(t)); !errors.Is(err, ErrAborted) {
		t.Fatal("aborted commit", err)
	}
	if got := ValueInteger(RowValues(rows(t, s, "SELECT count(*) FROM transaction_test")[0])[0]); got != 1 {
		t.Fatal("aborted changes committed", got)
	}
	tx, err = Begin(s, 2, true, control(t))
	if err != nil {
		t.Fatal(err)
	}
	got := rows(t, tx, "SHOW transaction_isolation")
	if ValueText(RowValues(got[0])[0]) != "serializable" {
		t.Fatal(got)
	}
	_, _, err = Execute(tx, "CREATE TABLE forbidden_readonly(id int)", nil, nil, control(t))
	requireState(t, err, "25006")
	if err = Rollback(tx); err != nil {
		t.Fatal(err)
	}
	exec(t, s, "CREATE TEMP TABLE deferred_test(id int UNIQUE DEFERRABLE INITIALLY DEFERRED)")
	tx, err = Begin(s, 0, false, control(t))
	if err != nil {
		t.Fatal(err)
	}
	exec(t, tx, "INSERT INTO deferred_test VALUES(1),(1)")
	err = Commit(tx, control(t))
	requireState(t, err, "23505")
	_, _, depth := Resources(s)
	if depth != 0 {
		t.Fatal("failed commit retained ended transaction")
	}
	rows(t, s, "SELECT 1")
}
func TestSQLBoundariesAreServerChecked(t *testing.T) {
	s := connect(t)
	for _, text := range []string{"BEGIN", "-- comment\n COMMIT", "/*a/*b*/c*/ROLLBACK", "; BEGIN", "/* c */ ; COMMIT", "START TRANSACTION", "PREPARE TRANSACTION 'x'", "CALL f()", "DO $$BEGIN COMMIT; END$$", "COPY x FROM STDIN"} {
		_, _, err := Execute(s, text, nil, nil, control(t))
		requireClass(t, err, 3)
	}
	for _, text := range []string{"SELECT 1; SELECT 2", "CREATE TEMP TABLE forbidden_multi(id int); INSERT INTO forbidden_multi VALUES(1)"} {
		_, _, err := Execute(s, text, nil, nil, control(t))
		if err == nil {
			t.Fatal("multiple accepted", text)
		}
	}
	r := rows(t, s, "SELECT count(*) FROM pg_class WHERE relname='forbidden_multi'")
	if ValueInteger(RowValues(r[0])[0]) != 0 {
		t.Fatal("partial effects")
	}
	for _, text := range []string{"SELECT ';COMMIT'::text", "SELECT $tag$;ROLLBACK$tag$::text", `/*outer /*inner*/ done*/ SELECT E'a\';COMMIT'::text`} {
		rows(t, s, text)
	}
}
func TestCancellationTimeoutGateAndClose(t *testing.T) {
	s := connect(t)
	c := control(t)
	Cancel(c)
	_, _, err := Execute(s, "SELECT 1", nil, nil, c)
	requireClass(t, err, 6)
	rows(t, s, "SELECT 1")
	short, err := NewControl(30)
	if err != nil {
		t.Fatal(err)
	}
	defer Cancel(short)
	start := time.Now()
	_, _, err = Execute(s, "SELECT pg_sleep(5)", nil, nil, short)
	requireClass(t, err, 5)
	if time.Since(start) > 2*time.Second {
		t.Fatal("timeout not observed")
	}
	// pgx's default context watcher may close the physical connection. A failed
	// socket is marked Closed and is never replayed on a replacement connection.
	if IsClosed(s) {
		_, _, err = Execute(s, "SELECT 1", nil, nil, control(t))
		requireClass(t, err, 1)
	} else {
		rows(t, s, "SELECT 1")
	}
	s = connect(t)
	root := s.(*SessionState).db
	root.mu.Lock()
	short, err = NewControl(20)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = Execute(s, "SELECT 1", nil, nil, short)
	Cancel(short)
	root.mu.Unlock()
	requireClass(t, err, 5)
	rows(t, s, "SELECT 1")
	s = connect(t)
	active := control(t)
	done := make(chan error, 1)
	go func() { _, _, err := Execute(s, "SELECT pg_sleep(5)", nil, nil, active); done <- err }()
	time.Sleep(30 * time.Millisecond)
	start = time.Now()
	closeErr := Close(s)
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("close did not interrupt")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("close blocked")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("slow close")
	}
	if closeErr != nil && !IsClosed(s) {
		t.Fatal(closeErr)
	}
	if !IsClosed(s) {
		t.Fatal("still open")
	}
}
func TestConcurrentCallsSerializeAndControlsRemainIndependent(t *testing.T) {
	s := connect(t)
	exec(t, s, "CREATE TEMP TABLE concurrent_test(i int)")
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, _ := NewControl(5000)
			defer Cancel(c)
			_, _, err := Execute(s, "INSERT INTO concurrent_test VALUES($1)", []Value{Integer(int64(i))}, nil, c)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if ValueInteger(RowValues(rows(t, s, "SELECT count(*) FROM concurrent_test")[0])[0]) != 20 {
		t.Fatal("lost writes")
	}
	c := control(t)
	cancelled := control(t)
	Cancel(cancelled)
	if _, _, err := Execute(s, "SELECT 1", nil, nil, cancelled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	_, _, err := Execute(s, "SELECT 1", nil, nil, c)
	if err != nil {
		t.Fatal(err)
	}
}
func TestLocalValidation(t *testing.T) {
	for _, n := range []int64{-1, 9223372036854775807} {
		if _, err := NewControl(n); !errors.Is(err, ErrArgument) {
			t.Fatal(n, err)
		}
	}
	for _, sql := range []string{"", strings.Repeat("x", 1048577), "SELECT\x001", "/*", "-- only"} {
		if err := sqlText(sql); !errors.Is(err, ErrArgument) {
			t.Fatal(sql[:min(len(sql), 20)], err)
		}
	}
}

func TestServerVersionAndRootCloseRollsBack(t *testing.T) {
	s := connect(t)
	version := ValueInteger(RowValues(rows(t, s, "SELECT current_setting('server_version_num')::bigint")[0])[0])
	if version < 160000 || version >= 170000 {
		t.Fatalf("PostgreSQL 16 required, server_version_num=%d", version)
	}
	table := fmt.Sprintf("goml_rollback_%d_%d", os.Getpid(), time.Now().UnixNano())
	observer := connect(t)
	exec(t, s, "CREATE TABLE "+table+"(id int)")
	t.Cleanup(func() {
		_, _, err := Execute(observer, "DROP TABLE IF EXISTS "+table, nil, nil, control(t))
		if err != nil {
			t.Error(err)
		}
	})
	tx, err := Begin(s, 0, false, control(t))
	if err != nil {
		t.Fatal(err)
	}
	exec(t, tx, "INSERT INTO "+table+" VALUES(1)")
	st, err := Prepare(tx, "SELECT id FROM "+table, control(t))
	if err != nil {
		t.Fatal(err)
	}
	c, err := QueryStatement(st, nil, nil, control(t))
	if err != nil {
		t.Fatal(err)
	}
	if err = Close(s); err != nil {
		t.Fatal(err)
	}
	if !IsClosed(s) {
		t.Fatal("connection remained open")
	}
	if a, b, d := Resources(s); a != 0 || b != 0 || d != 0 {
		t.Fatal(a, b, d)
	}
	_, _, err = Next(c)
	requireClass(t, err, 1)
	_, _, err = ExecuteStatement(st, nil, nil, control(t))
	requireClass(t, err, 1)
	if err = Rollback(tx); err != nil {
		t.Fatal(err)
	}
	count := ValueInteger(RowValues(rows(t, observer, "SELECT count(*) FROM "+table)[0])[0])
	if count != 0 {
		t.Fatalf("close committed %d rows", count)
	}
}

func TestPreparedExecutionAndCursorFailures(t *testing.T) {
	s := connect(t)
	exec(t, s, "CREATE TEMP TABLE prepared_test(id bigint PRIMARY KEY)")
	st, err := Prepare(s, "INSERT INTO prepared_test VALUES($1)", control(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, number := range []int64{1, 1, 2} {
		n, id, err := ExecuteStatement(st, []Value{Integer(number)}, nil, control(t))
		if number == 1 && n == 0 {
			requireState(t, err, "23505")
		} else if err != nil || n != 1 || id != 0 {
			t.Fatal(n, id, err)
		}
	}
	if err = CloseStatement(st); err != nil {
		t.Fatal(err)
	}
	if ValueInteger(RowValues(rows(t, s, "SELECT count(*) FROM prepared_test")[0])[0]) != 2 {
		t.Fatal("prepared reuse")
	}
	c, err := Query(s, "SELECT 12/(3-i) FROM generate_series(1,5) i", nil, nil, control(t))
	if err != nil {
		t.Fatal(err)
	}
	var saved Row
	for {
		ok, row, e := Next(c)
		if e != nil {
			requireState(t, e, "22012")
			break
		}
		if !ok {
			t.Fatal("missing division error")
		}
		saved = row
	}
	_, _, err = Next(c)
	requireState(t, err, "22012")
	if err = CloseCursor(c); err != nil {
		t.Fatal(err)
	}
	if ValueInteger(RowValues(saved)[0]) != 12 {
		t.Fatal("returned row changed")
	}
	rows(t, s, "SELECT 1")
	if _, n, _ := Resources(s); n != 0 {
		t.Fatal("failed cursor retained")
	}
	control := control(t)
	c, err = Query(s, "SELECT i FROM generate_series(1,3) i", nil, nil, control)
	if err != nil {
		t.Fatal(err)
	}
	Cancel(control)
	_, _, err = Next(c)
	requireClass(t, err, 6)
	_, _, again := Next(c)
	if again != err {
		t.Fatal("first cursor error not retained")
	}
	if _, n, _ := Resources(s); n != 0 {
		t.Fatal("cancelled cursor retained")
	}
	if !IsClosed(s) {
		rows(t, s, "SELECT 1")
	}
}
