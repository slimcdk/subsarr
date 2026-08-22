// Package catalogue reads the SQL dump that ships inside the Subscene archive.
//
// The dump is a mysqldump of one table with a row per Subscene upload: the title
// Subscene showed, the IMDB id, the release names the uploader listed, the
// uploader, the comment, the date, the language, and the path of the file that
// holds the subtitle. Everything subsarr knows about an upload comes from here;
// the file names alone gave none of it.
package catalogue

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Row is one raw catalogue row: the dump's own values, keyed by the meaning this
// package worked out for each column. Interpretation (IMDB formatting, dates,
// release lists) happens in Mapper.
type Row map[string]string

// statement is one SQL statement's leading text, up to its first value list.
type reader struct {
	br *bufio.Reader
}

// tokenizer states.
const (
	quote        = '\''
	escape       = '\\'
	openParen    = '('
	closeParen   = ')'
	statementEnd = ';'
)

// scan walks the dump, calling onColumns for each CREATE TABLE it finds and
// onTuple for each value list of each INSERT.
//
// It is a tokenizer rather than a regular expression because the values contain
// everything a user could type into a comment field — apostrophes, backslashes,
// semicolons, newlines, unbalanced parentheses — and only quote tracking tells a
// closing bracket from a character in a film's title.
func (r *reader) scan(onColumns func(table string, columns []string) error, onTuple func([]string) error) error {
	for {
		head, delim, err := r.readHead()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		upper := strings.ToUpper(head)
		switch {
		case delim == openParen && strings.Contains(upper, "CREATE TABLE"):
			columns, err := r.readColumnDefinitions()
			if err != nil {
				return err
			}
			if err := onColumns(identifier(head, "TABLE"), columns); err != nil {
				return err
			}

		case delim == openParen && strings.Contains(upper, "INSERT INTO"):
			// `INSERT INTO t (a, b) VALUES (...)` — the first bracket is the
			// column list, and the value lists follow it.
			values, err := r.readTuple()
			if err != nil {
				return err
			}
			if !strings.Contains(upper, "VALUES") {
				// The bracket held the column list; the value lists come after
				// the VALUES keyword that follows it.
				if err := onColumns(identifier(head, "INTO"), unquoteAll(values)); err != nil {
					return err
				}
				_, delim, err := r.readHead()
				if err != nil {
					return ignoreEOF(err)
				}
				if delim != openParen {
					continue
				}
				if values, err = r.readTuple(); err != nil {
					return err
				}
			}
			if err := onTuple(values); err != nil {
				return err
			}
			if err := r.readValueLists(onTuple); err != nil {
				return err
			}
		}
	}
}

// readHead reads up to the next `(` or `;` that is not inside a string literal,
// returning the text before it. String contents are skipped rather than kept: a
// bracket in a film's title must not look like the start of a value list.
func (r *reader) readHead() (string, byte, error) {
	var b strings.Builder
	quoted := false
	for {
		c, err := r.br.ReadByte()
		if err != nil {
			if err == io.EOF && b.Len() > 0 {
				return b.String(), statementEnd, nil
			}
			return "", 0, err
		}

		if quoted {
			switch c {
			case escape:
				r.br.ReadByte()
			case quote:
				quoted = false
			}
			continue
		}

		switch c {
		case quote:
			quoted = true
		case openParen, statementEnd:
			return b.String(), c, nil
		default:
			b.WriteByte(c)
		}
	}
}

// readValueLists reads the `,(...)` repetitions that follow the first value list
// of an extended INSERT, stopping at the statement's semicolon.
func (r *reader) readValueLists(onTuple func([]string) error) error {
	for {
		c, err := r.skipSpace()
		if err != nil {
			return ignoreEOF(err)
		}
		switch c {
		case ',':
			next, err := r.skipSpace()
			if err != nil {
				return ignoreEOF(err)
			}
			if next != openParen {
				return fmt.Errorf("expected a value list after a comma, got %q", next)
			}
			values, err := r.readTuple()
			if err != nil {
				return err
			}
			if err := onTuple(values); err != nil {
				return err
			}
		case statementEnd:
			return nil
		default:
			// Anything else ends the statement as far as we are concerned.
			return nil
		}
	}
}

func (r *reader) skipSpace() (byte, error) {
	for {
		c, err := r.br.ReadByte()
		if err != nil {
			return 0, err
		}
		switch c {
		case ' ', '\t', '\r', '\n':
			continue
		default:
			return c, nil
		}
	}
}

// readTuple reads one parenthesised, comma-separated list of SQL literals. The
// opening bracket has already been consumed.
func (r *reader) readTuple() ([]string, error) {
	var (
		values    []string
		current   strings.Builder
		quoted    bool
		wasQuoted bool
	)

	for {
		c, err := r.br.ReadByte()
		if err != nil {
			return nil, ignoreEOF(err)
		}

		if quoted {
			switch c {
			case escape:
				next, err := r.br.ReadByte()
				if err != nil {
					return nil, ignoreEOF(err)
				}
				current.WriteByte(unescape(next))
			case quote:
				// A doubled quote is a literal quote, not the end of the string.
				next, err := r.br.Peek(1)
				if err == nil && next[0] == quote {
					r.br.ReadByte()
					current.WriteByte(quote)
					continue
				}
				quoted = false
				wasQuoted = true
			default:
				current.WriteByte(c)
			}
			continue
		}

		switch c {
		case quote:
			quoted = true
		case ',':
			values = append(values, finish(current.String(), wasQuoted))
			current.Reset()
			wasQuoted = false
		case closeParen:
			values = append(values, finish(current.String(), wasQuoted))
			return values, nil
		case ' ', '\t', '\r', '\n':
			// Padding around a value is not part of it; padding inside an
			// unquoted token (there is no such thing in a dump) is kept.
			if current.Len() > 0 && !wasQuoted {
				current.WriteByte(' ')
			}
		default:
			current.WriteByte(c)
		}
	}
}

// readColumnDefinitions reads the body of a CREATE TABLE and returns the column
// names in order, ignoring the key and constraint clauses.
func (r *reader) readColumnDefinitions() ([]string, error) {
	var (
		columns []string
		line    strings.Builder
		depth   int
		quoted  bool
	)

	flush := func() {
		name := columnName(line.String())
		if name != "" {
			columns = append(columns, name)
		}
		line.Reset()
	}

	for {
		c, err := r.br.ReadByte()
		if err != nil {
			return columns, ignoreEOF(err)
		}

		if quoted {
			if c == escape {
				r.br.ReadByte()
				continue
			}
			if c == quote {
				quoted = false
			}
			continue
		}

		switch c {
		case quote:
			quoted = true
		case openParen:
			depth++
		case closeParen:
			if depth == 0 {
				flush()
				return columns, nil
			}
			depth--
		case ',':
			if depth == 0 {
				flush()
				continue
			}
		default:
			line.WriteByte(c)
		}
	}
}

// columnName pulls the column name out of one line of a CREATE TABLE body, or
// returns "" when the line declares a key rather than a column.
func columnName(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	upper := strings.ToUpper(line)
	for _, keyword := range []string{"PRIMARY KEY", "UNIQUE KEY", "KEY ", "INDEX ", "CONSTRAINT", "FULLTEXT"} {
		if strings.HasPrefix(upper, keyword) {
			return ""
		}
	}
	name, _, _ := strings.Cut(line, " ")
	return unquoteIdentifier(name)
}

// identifier returns the name that follows a keyword, as in `INSERT INTO x` or
// `CREATE TABLE x`.
func identifier(head, keyword string) string {
	upper := strings.ToUpper(head)
	i := strings.Index(upper, keyword)
	if i < 0 {
		return ""
	}
	rest := strings.TrimSpace(head[i+len(keyword):])
	name, _, _ := strings.Cut(rest, " ")
	return unquoteIdentifier(name)
}

func unquoteAll(names []string) []string {
	out := make([]string, len(names))
	for i, name := range names {
		out[i] = unquoteIdentifier(name)
	}
	return out
}

func unquoteIdentifier(name string) string {
	return strings.Trim(strings.TrimSpace(name), "`\"[]")
}

// finish renders a scanned value. An unquoted NULL is an absent value, which is
// not the same as the string "NULL" a quoted one would be.
func finish(raw string, quoted bool) string {
	if quoted {
		return raw
	}
	trimmed := strings.TrimSpace(raw)
	if strings.EqualFold(trimmed, "NULL") {
		return ""
	}
	return trimmed
}

func unescape(c byte) byte {
	switch c {
	case 'n':
		return '\n'
	case 't':
		return '\t'
	case 'r':
		return '\r'
	case '0':
		return 0
	default:
		return c
	}
}

func ignoreEOF(err error) error {
	if err == io.EOF {
		return nil
	}
	return err
}
