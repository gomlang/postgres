package adapter

func GomlBind_raw_begin(p0 Session, p1 int, p2 bool, p3 Control) (Session, Failure) {
    return BridgeBegin(p0, p1, p2, p3)
}

func GomlBind_raw_blob(p0 []uint8) Value {
    return Blob(p0)
}

func GomlBind_raw_cancel(p0 Control) {
    Cancel(p0)
}

func GomlBind_raw_close(p0 Session) Failure {
    return BridgeClose(p0)
}

func GomlBind_raw_close_cursor(p0 Cursor) Failure {
    return BridgeCloseCursor(p0)
}

func GomlBind_raw_close_statement(p0 Statement) Failure {
    return BridgeCloseStatement(p0)
}

func GomlBind_raw_column_name(p0 Column) string {
    return ColumnName(p0)
}

func GomlBind_raw_column_type(p0 Column) string {
    return ColumnType(p0)
}

func GomlBind_raw_commit(p0 Session, p1 Control) Failure {
    return BridgeCommit(p0, p1)
}

func GomlBind_raw_cursor_columns(p0 Cursor) []Column {
    return CursorColumns(p0)
}

func GomlBind_raw_error_class(p0 Failure) int {
    return FailureClass(p0)
}

func GomlBind_raw_error_is_nil(p0 Failure) bool {
    return FailureIsNil(p0)
}

func GomlBind_raw_error_message(p0 Failure) string {
    return FailureMessage(p0)
}

func GomlBind_raw_error_state(p0 Failure) string {
    return FailureSQLState(p0)
}

func GomlBind_raw_execute(p0 Session, p1 string, p2 []Value, p3 []string, p4 Control) (int64, int64, Failure) {
    return BridgeExecute(p0, p1, p2, p3, p4)
}

func GomlBind_raw_execute_statement(p0 Statement, p1 []Value, p2 []string, p3 Control) (int64, int64, Failure) {
    return BridgeExecuteStatement(p0, p1, p2, p3)
}

func GomlBind_raw_integer(p0 int64) Value {
    return Integer(p0)
}

func GomlBind_raw_is_closed(p0 Session) bool {
    return IsClosed(p0)
}

func GomlBind_raw_new_control(p0 int64) (Control, Failure) {
    return BridgeNewControl(p0)
}

func GomlBind_raw_next(p0 Cursor) (bool, Row, Failure) {
    return BridgeNext(p0)
}

func GomlBind_raw_null() Value {
    return Null()
}

func GomlBind_raw_open(p0 string, p1 Control) (Session, Failure) {
    return BridgeOpen(p0, p1)
}

func GomlBind_raw_prepare(p0 Session, p1 string, p2 Control) (Statement, Failure) {
    return BridgePrepare(p0, p1, p2)
}

func GomlBind_raw_query(p0 Session, p1 string, p2 []Value, p3 []string, p4 Control) (Cursor, Failure) {
    return BridgeQuery(p0, p1, p2, p3, p4)
}

func GomlBind_raw_query_statement(p0 Statement, p1 []Value, p2 []string, p3 Control) (Cursor, Failure) {
    return BridgeQueryStatement(p0, p1, p2, p3)
}

func GomlBind_raw_real(p0 float64) Value {
    return Real(p0)
}

func GomlBind_raw_resources(p0 Session) (int, int, int) {
    return Resources(p0)
}

func GomlBind_raw_rollback(p0 Session) Failure {
    return BridgeRollback(p0)
}

func GomlBind_raw_row_columns(p0 Row) []Column {
    return RowColumns(p0)
}

func GomlBind_raw_row_values(p0 Row) []Value {
    return RowValues(p0)
}

func GomlBind_raw_text(p0 string) Value {
    return Text(p0)
}

func GomlBind_raw_value_blob(p0 Value) []uint8 {
    return ValueBlob(p0)
}

func GomlBind_raw_value_integer(p0 Value) int64 {
    return ValueInteger(p0)
}

func GomlBind_raw_value_kind(p0 Value) int {
    return ValueKind(p0)
}

func GomlBind_raw_value_real(p0 Value) float64 {
    return ValueReal(p0)
}

func GomlBind_raw_value_text(p0 Value) string {
    return ValueText(p0)
}
