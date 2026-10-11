# postgres

`ecosystem::postgres` is a managed PostgreSQL adapter for the shared
`ecosystem::sql` contracts. Its explicit Go FFI bridge uses
[`pgx/v5 v5.11.0`](https://github.com/jackc/pgx/releases/tag/v5.11.0) through
[`database/sql`](https://pkg.go.dev/github.com/jackc/pgx/v5/stdlib).
The adapter owns one physical connection and implements `sql::Driver`,
`Connection`, `Executor`, `Transaction` and `RowCursor` directly.

## Setup

```toml
[dependencies]
"ecosystem::postgres" = true
"ecosystem::sql" = true
```

GoML dependencies are unversioned: `true` tracks each package repository’s default branch.

The native module is `example.com/goml-ecosystem/postgres`, declared by the
library's `[native]` manifest. It is local ecosystem infrastructure, not a
published Go module. `go.mod` and `go.sum` pin pgx and its dependencies; download
these explicitly before GoML's offline FFI/build checks. A native application
can use the independent `testdata/downstream/native` fixture as its manifest
and Go module example. The `sql` module currently has a transitive SQLite
dependency; this adapter uses no SQLite connection or engine.

```goml
use ecosystem::postgres::{PostgresDriver};
use ecosystem::sql::{Driver, Connection, Executor, Query, Params, Value, Row, Error};

fn lookup(dsn: string, id: i64) -> Result[string, Error] {
    let connection = PostgresDriver::new().connect(dsn)?;
    defer { let _ = connection.close(); };
    let query = Query::new("SELECT name FROM users WHERE id=$1",
        Params::positional(Vec::from_array([Value::Integer(id)])))?;
    let row: Row = connection.query_one(query)?;
    row.get(0)
}
```

DSNs use pgx's PostgreSQL URI or keyword/value format, including its TLS options.
The adapter requests UTF-8 client encoding. DSNs and SQL text are configuration;
parameter values use driver binding, never interpolation.

## API

| API | Behavior |
| --- | --- |
| `PostgresDriver::new().connect(dsn)` / `PostgresConnection::open` | Open and ping one physical connection |
| `open_with(dsn, control)` | Cancellable connection establishment |
| `sql::Executor::execute/query` | Execute a `sql::Query` or return `PostgresRows` |
| `execute_with/query_with(query, control)` | Explicit cancellation/deadline, also on transactions |
| `prepare/prepare_with` | Return a reusable `Statement` owned by this connection or transaction |
| `Statement.execute/query(params)` and `_with` variants | Rebind positional parameters |
| `PostgresRows.columns()` | Names and PostgreSQL declared type names, including empty results |
| `sql::RowCursor::next/close` | Owned rows, repeated EOF, explicit idempotent cleanup |
| `sql::Executor::query_one/query_optional/query_all/query_all_with_limits` | Shared typed conversion and bounded collection with cursor cleanup |
| `begin(TransactionMode::Deferred)` | Read committed, read/write transaction |
| `begin_with(TransactionOptions, Control)` | Read committed, repeatable read or serializable; optional read-only |
| `transaction(callback)` | Commit success; deferred rollback on error or unwinding |
| `PostgresTransaction.commit/commit_with/rollback/close` | Finish the managed transaction |
| `PostgresConnection.resources/is_closed/close` | Diagnostics and deterministic teardown |

Import the relevant `ecosystem::sql` traits when using their methods. The shared
`Row`, `FromRow`, `FromValue`, `Query`, `Params`, `Execution` and `Error` types
cross the module boundary unchanged. Statements take `Params`; their SQL text
was fixed by `prepare`. They are not themselves `sql::Executor` implementations.

## SQL and values

Calls accept one statement up to 1 MiB, using at most 65,535 positional `$1`, `$2`,
… parameters. Named parameters are rejected. Both ad-hoc and reusable calls
prepare through PostgreSQL's extended protocol before execution; the server
rejects multiple statements before any of them execute. Dollar-quoted strings,
escape strings and nested comments retain PostgreSQL's normal parsing rules.
Leading empty statements are rejected. Direct transaction commands, SQL-level
PREPARE/EXECUTE/DEALLOCATE, DISCARD, COPY, CALL and DO are excluded from this
managed API. Use the transaction methods to manage transaction state.

`Execution.rows_affected` comes from the command tag. `last_insert_id` is always
zero: use `INSERT ... RETURNING` through `query` to retrieve generated keys.

| PostgreSQL result | Shared `sql::Value` |
| --- | --- |
| NULL | `Null`, distinct from empty text and empty bytea |
| smallint/integer/bigint | `Integer(i64)` |
| real/double precision | `Real(f64)` |
| boolean | `Integer(0/1)`, accepted by shared `FromValue[bool]` |
| bytea | `Blob(Bytes)` |
| text, numeric, UUID, JSON/JSONB, arrays and other driver text results | `Text(string)` |
| date | ISO date text |
| timestamp without time zone | ISO date/time text without a zone suffix |
| timestamptz | UTC RFC3339Nano text |

Numeric values remain exact text; they are not silently rounded to float64.
Array/range/composite values have no structured shared representation; select a
text cast when a stable textual interface is required. Temporal text describes
the driver's decoded value, not the original input spelling. PostgreSQL infinity
values follow the driver's textual representation. Unsupported native values
produce a recoverable `Conversion` error and close the cursor.

`Value::Integer`, `Real`, `Text`, `Blob` and `Null` bind their corresponding Go
scalar values. Use explicit SQL casts where PostgreSQL cannot infer a type.
The shared model has no Boolean parameter variant: shared Boolean conversion
produces integer 0/1; `($1::bigint <> 0)` converts that to PostgreSQL boolean.
For other types, text-mediated casts such as `$1::text::numeric` make the input
policy explicit. Text, including `TextBytes`, must be valid UTF-8 without NUL;
use bytea for arbitrary bytes. Binary parameters and result values are copied.
A saved row remains valid after cursor advancement, closure and later queries;
shared row accessors also return independent binary values.

## Lifecycle and failure

Calls on one connection serialize. A live cursor makes new execution,
preparation and transaction creation return `Busy`, instead of waiting for the
connection it owns. While a transaction is active, root connection operations
return `Busy`. Finish or roll back the transaction before using the root again.
SQLite's Immediate/Exclusive transaction modes are explicitly rejected.
Nested transactions/savepoints and a connection pool are not exposed.

Ordinary PostgreSQL statement errors leave an otherwise healthy connection
usable. Within a transaction PostgreSQL can enter its aborted state: subsequent
operations retain the server's `25P02` error until rollback. Committing such a
transaction rolls it back and reports failure, rather than reporting a successful
commit. A failed commit that ended the native transaction invalidates its handles.
Rollback cleans up independently of an already-cancelled operation control; if it
cannot establish an idle connection, the adapter discards that connection.

Closing a statement closes its cursor. Closing/draining a cursor releases its
connection occupancy; normal close drains the driver's pending result, so it may
wait for server work. Use the cursor's original control to interrupt long work.
A connection close cancels ongoing operations, closes its rows/statements,
rolls back unfinished transactions and releases both the connection and native
pool. Close/rollback are idempotent; ordinary operations on ended handles fail
with `Closed`. A failed cursor retains its first iteration error; a drained cursor
retains repeated EOF. Garbage collection is not a resource-closing API.

`Control::unlimited()` is cancellable. `Control::timeout(ms)` starts the deadline
at construction; zero means no deadline. `cancel()` is safe from another task.
Cancel timeout controls after use to release timers. A query's control remains
active during iteration. The control used for `begin_with` governs transaction
creation, not its entire lifetime. Waiting for the connection gate observes the
same cancellation/deadline. Cleanup has its own bounded rollback context.

pgx's default cancellation policy can close the physical connection. The adapter
marks an unusable connection `Closed`; callers must reconnect explicitly. It does
not replay SQL on a new connection. Cancellation and deadlines are cooperative,
not hard real-time limits. Errors use shared `sql::ErrorKind`; PostgreSQL errors
include SQLSTATE in `Error.source`. The native `FailureError` preserves the
original error for Go `errors.Is`/`errors.As` inspection. Server lock-not-available
maps to `Busy`; an explicit control's timeout/cancellation maps to `Timeout` or
`Cancelled`. When a connection-closed cause is also present, `Closed` takes
precedence over timeout/cancellation; the native error chain retains both causes.

## Verification

Go 1.26 and the source-built GoML toolchain with unversioned registry support pinned in [verification/ci/toolchain.json](https://github.com/gomlang/verification/blob/main/ci/toolchain.json) are the validation baseline. No toolchain modifications
are required. Tests intentionally fail without `GOML_POSTGRES_TEST_DSN`: the live
PostgreSQL gate cannot silently pass without a database. Use an isolated PostgreSQL
16 database. Tests use temporary tables except for one uniquely named rollback
check that removes its own table.

With sibling `verification`, `sql`, and `sqlite` checkouts available, download
the complete native dependency closure before verification. This includes the
SQLite adapter reached through the SQL library and the independent fixture.

```sh
python3 ../verification/ci/ecosystem.py native --libraries .. --module postgres
# Recreate the checked-in bridge from its explicit allowlist.
goml bind-go bindings.json
python3 scripts/with-postgres.py go test ./adapter -count=1
python3 scripts/with-postgres.py go test -race ./adapter -count=1
# GOML_HOME must resolve the ecosystem::sql dependency and native mapping.
python3 scripts/with-postgres.py goml test --timeout 300s
python3 scripts/with-postgres.py goml run --example basic
```

`with-postgres.py` uses an existing explicit DSN, or starts its own official
`postgres:16` Docker container on a random loopback port, then removes only that
container in `finally`. It records image/version and cleanup under `_artifact`.
The independent fixture exercises the published module boundary, custom
`FromValue`/`FromRow`, typed errors and resource cleanup. Native tests additionally
exercise actual PostgreSQL statement/transaction errors, cancellation, concurrent
calls and explicit closure; race tests cover the same native API paths.
