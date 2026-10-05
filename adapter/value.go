package adapter

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

type Value struct {
	kind    int
	integer int64
	real    float64
	text    string
	blob    []byte
}

func Null() Value {
	return Value{}
}
func Integer(value int64) Value {
	return Value{kind: 1, integer: value}
}
func Real(value float64) Value {
	return Value{kind: 2, real: value}
}
func Text(value string) Value {
	return Value{kind: 3, text: value}
}
func Blob(value []byte) Value {

	data := make([]byte, len(value))
	copy(data, value)
	return Value{kind: 4, blob: data}
}
func ValueKind(value Value) int {
	return value.kind
}
func ValueInteger(value Value) int64 {
	return value.integer
}
func ValueReal(value Value) float64 {
	return value.real
}
func ValueText(value Value) string {
	return value.text
}
func ValueBlob(value Value) []byte {

	result := make([]byte, len(value.blob))
	copy(result, value.blob)
	return result
}
func (v Value) native() (any, error) {
	switch v.kind {
	case 0:
		return nil, nil
	case 1:
		return v.integer, nil
	case 2:
		return v.real, nil
	case 3:
		return v.text, nil
	case 4:
		result := make([]byte, len(v.blob))
		copy(result, v.blob)
		return result, nil
	default:
		return nil, ErrType
	}
}

func fromNative(value any, declared string) (Value, error) {
	switch value := value.(type) {
	case nil:
		return Null(), nil
	case int64:
		return Integer(value), nil
	case float64:
		return Real(value), nil
	case bool:
		if value {
			return Integer(1), nil
		}
		return Integer(0), nil
	case string:
		return Text(value), nil
	case []byte:
		if declared == "BYTEA" {
			return Blob(value), nil
		}
		return Text(string(value)), nil
	case time.Time:
		switch declared {
		case "DATE":
			return Text(value.Format("2006-01-02")), nil
		case "TIMESTAMP":
			return Text(value.Format("2006-01-02T15:04:05.999999999")), nil
		default:
			return Text(value.UTC().Format(time.RFC3339Nano)), nil
		}
	default:
		return Value{}, fmt.Errorf("%w: %T", ErrType, value)
	}
}
func parameters(values []Value, names []string) ([]any, error) {
	if len(names) != 0 || len(values) > 65535 {
		return nil, fmt.Errorf("%w: use at most 65535 positional parameters", ErrArgument)
	}
	result := make([]any, len(values))
	for i, value := range values {
		if value.kind == 3 && (!utf8.ValidString(value.text) || strings.IndexByte(value.text, 0) >= 0) {
			return nil, fmt.Errorf("%w: PostgreSQL text must be valid UTF-8 without NUL", ErrType)
		}
		v, err := value.native()
		if err != nil {
			return nil, err
		}
		result[i] = v
	}
	return result, nil
}

type Column struct{ name, declared string }

func ColumnName(c Column) string { return c.name }
func ColumnType(c Column) string { return c.declared }

type Row struct {
	values  []Value
	columns []Column
}

func RowValues(r Row) []Value   { return append([]Value(nil), r.values...) }
func RowColumns(r Row) []Column { return append([]Column(nil), r.columns...) }
