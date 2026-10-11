# PostgreSQL native consumer

This independent module imports `ecosystem::postgres` and the shared SQL traits.
It requires `GOML_POSTGRES_TEST_DSN` pointing to PostgreSQL 16. `main` checks a
real scalar query; the consumer test implements its own `FromValue`/`FromRow` and
checks conversion-error cleanup across the module boundary.

The Go module uses a local native adapter replacement. GoML dependencies resolve
through the verification registry; the ecosystem verification runner supplies the current library
snapshot. No database mock, compiler change or hidden server fallback is used.
