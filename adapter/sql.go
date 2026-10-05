package adapter

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// The server's Parse message enforces the single-statement boundary. This scan
// only identifies the leading command, so SQL cannot bypass managed resources.
func sqlText(text string) error {
	if len(text) == 0 || len(text) > 1048576 || !utf8.ValidString(text) || strings.IndexByte(text, 0) >= 0 {
		return fmt.Errorf("%w: SQL text", ErrArgument)
	}
	i := 0
	for i < len(text) {
		switch text[i] {
		case ' ', '\t', '\r', '\n', '\f', '\v':
			i++
			continue
		}
		if strings.HasPrefix(text[i:], "--") {
			for i < len(text) && text[i] != '\n' && text[i] != '\r' {
				i++
			}
			continue
		}
		if strings.HasPrefix(text[i:], "/*") {
			i += 2
			depth := 1
			for i < len(text) && depth > 0 {
				if strings.HasPrefix(text[i:], "/*") {
					depth++
					i += 2
				} else if strings.HasPrefix(text[i:], "*/") {
					depth--
					i += 2
				} else {
					i++
				}
			}
			if depth != 0 {
				return fmt.Errorf("%w: unterminated SQL comment", ErrArgument)
			}
			continue
		}
		break
	}
	if i == len(text) {
		return fmt.Errorf("%w: empty SQL statement", ErrArgument)
	}
	start := i
	for i < len(text) && (text[i] >= 'a' && text[i] <= 'z' || text[i] >= 'A' && text[i] <= 'Z' || text[i] == '_') {
		i++
	}
	if i == start && text[start] != '(' {
		return fmt.Errorf("%w: expected SQL command", ErrArgument)
	}
	switch strings.ToUpper(text[start:i]) {
	case "BEGIN", "START", "COMMIT", "END", "ABORT", "ROLLBACK", "SAVEPOINT", "RELEASE", "PREPARE", "EXECUTE", "DEALLOCATE", "DISCARD", "COPY", "CALL", "DO":
		return fmt.Errorf("%w: command requires a dedicated managed API", ErrArgument)
	}
	return nil
}
