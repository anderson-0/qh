// Package mongodb is the MongoDB engine. Queries are a fixed subset of mongosh
// syntax, parsed here into a Call; no JavaScript is ever executed.
package mongodb

import (
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/anderson-0/qh/internal/qerr"
)

const (
	maxQueryBytes = 256 << 10
	maxDepth      = 100
)

// Call is one parsed mongosh call.
type Call struct {
	DB         string     // from getSiblingDB; "" means the connection's default database
	Collection string     // "" for database-level methods
	Method     string     // as written: "find", "getCollectionNames", ...
	Args       []any      // BSON-ready values
	Modifiers  []Modifier // chained cursor methods, in order
}

// Modifier is one chained cursor method, such as .sort({a: 1}).
type Modifier struct {
	Name string
	Args []any
}

// dbMethods are the methods called on db itself.
var dbMethods = map[string]bool{"getCollectionNames": true, "createCollection": true}

type parser struct {
	src   string
	pos   int
	depth int
}

// Parse parses one mongosh call. Errors are *qerr.Error values with category
// invalid; the hint shows the query line with a caret under the position.
func Parse(q string) (*Call, error) {
	if len(q) > maxQueryBytes {
		return nil, qerr.New(qerr.Invalid, "query is longer than 256 KiB")
	}
	p := &parser{src: q}
	c, err := p.call()
	if err != nil {
		return nil, err
	}
	p.skip()
	if p.peek() == ';' {
		p.pos++
		p.skip()
	}
	if p.pos < len(p.src) {
		return nil, p.errAt(p.pos, "only one statement per query")
	}
	return c, nil
}

func (p *parser) call() (*Call, error) {
	p.skip()
	if !p.word("db") {
		return nil, p.errAt(p.pos, `expected "db" (queries look like db.<collection>.find({...}))`)
	}
	c := &Call{}
	var names []string
	for {
		p.skip()
		switch p.peek() {
		case '[':
			p.pos++
			p.skip()
			s, err := p.str()
			if err != nil {
				return nil, err
			}
			if err := p.expect(']'); err != nil {
				return nil, err
			}
			names = append(names, s)
			continue
		case '.':
			p.pos++
		default:
			if len(names) == 0 {
				return nil, p.unexpected()
			}
			return nil, p.errAt(p.pos, "expected a method call such as .find(...)")
		}
		p.skip()
		at := p.pos
		id, err := p.ident()
		if err != nil {
			return nil, err
		}
		p.skip()
		if p.peek() != '(' {
			names = append(names, id)
			continue
		}
		args, err := p.args()
		if err != nil {
			return nil, err
		}
		switch {
		case len(names) == 0 && id == "getSiblingDB":
			if c.DB != "" || len(args) != 1 {
				return nil, p.errAt(at, "getSiblingDB takes one database name")
			}
			s, ok := args[0].(string)
			if !ok || s == "" {
				return nil, p.errAt(at, "getSiblingDB takes one database name")
			}
			c.DB = s
			continue
		case len(names) == 0 && id == "getCollection":
			s, ok := oneString(args)
			if !ok {
				return nil, p.errAt(at, "getCollection takes one collection name")
			}
			names = append(names, s)
			continue
		case len(names) == 0 && !dbMethods[id]:
			return nil, p.errAt(at, fmt.Sprintf("db.%s is not supported; call methods on a collection, as in db.<collection>.%s(...)", id, id))
		}
		c.Collection, c.Method, c.Args = strings.Join(names, "."), id, args
		break
	}
	for {
		p.skip()
		if p.peek() != '.' {
			return c, nil
		}
		p.pos++
		p.skip()
		id, err := p.ident()
		if err != nil {
			return nil, err
		}
		p.skip()
		if p.peek() != '(' {
			return nil, p.errAt(p.pos, fmt.Sprintf("expected ( after .%s", id))
		}
		args, err := p.args()
		if err != nil {
			return nil, err
		}
		c.Modifiers = append(c.Modifiers, Modifier{Name: id, Args: args})
	}
}

func oneString(args []any) (string, bool) {
	if len(args) != 1 {
		return "", false
	}
	s, ok := args[0].(string)
	return s, ok && s != ""
}

// args parses "(v, v, ...)" with an optional trailing comma.
func (p *parser) args() ([]any, error) {
	if err := p.expect('('); err != nil {
		return nil, err
	}
	out := []any{}
	for {
		p.skip()
		if p.peek() == ')' {
			p.pos++
			return out, nil
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		p.skip()
		switch p.peek() {
		case ',':
			p.pos++
		case ')':
		default:
			return nil, p.unexpected()
		}
	}
}

func (p *parser) value() (any, error) {
	p.skip()
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > maxDepth {
		return nil, p.errAt(p.pos, "nested more than 100 levels")
	}
	switch ch := p.peek(); {
	case ch == '{':
		return p.object()
	case ch == '[':
		return p.array()
	case ch == '"' || ch == '\'':
		return p.str()
	case ch == '/':
		return p.regex()
	case ch == '-' || ch == '.' || ch >= '0' && ch <= '9':
		return p.number()
	case isIdentStart(ch):
		return p.word2value()
	}
	return nil, p.unexpected()
}

func (p *parser) object() (bson.D, error) {
	p.pos++ // {
	d := bson.D{}
	for {
		p.skip()
		switch ch := p.peek(); {
		case ch == '}':
			p.pos++
			return d, nil
		case ch == '"' || ch == '\'':
		case isIdentStart(ch) || ch >= '0' && ch <= '9':
		default:
			return nil, p.unexpected()
		}
		var key string
		var err error
		if ch := p.peek(); ch == '"' || ch == '\'' {
			key, err = p.str()
		} else {
			key, err = p.keyWord()
		}
		if err != nil {
			return nil, err
		}
		if err := p.expect(':'); err != nil {
			return nil, err
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		if strings.ContainsRune(key, 0) {
			return nil, p.errAt(p.pos, "object keys cannot contain a NUL character")
		}
		d = append(d, bson.E{Key: key, Value: v})
		p.skip()
		switch p.peek() {
		case ',':
			p.pos++
		case '}':
		default:
			return nil, p.unexpected()
		}
	}
}

func (p *parser) array() (bson.A, error) {
	p.pos++ // [
	a := bson.A{}
	for {
		p.skip()
		if p.peek() == ']' {
			p.pos++
			return a, nil
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		a = append(a, v)
		p.skip()
		switch p.peek() {
		case ',':
			p.pos++
		case ']':
		default:
			return nil, p.unexpected()
		}
	}
}

func (p *parser) str() (string, error) {
	start := p.pos
	quote := p.peek()
	if quote != '"' && quote != '\'' {
		return "", p.unexpected()
	}
	p.pos++
	var b strings.Builder
	for p.pos < len(p.src) {
		ch := p.src[p.pos]
		switch {
		case ch == quote:
			p.pos++
			return b.String(), nil
		case ch == '\n':
			return "", p.errAt(start, "unterminated string")
		case ch == '\\':
			if p.pos+1 >= len(p.src) {
				return "", p.errAt(start, "unterminated string")
			}
			esc := p.src[p.pos+1]
			p.pos += 2
			switch esc {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case '0':
				b.WriteByte(0)
			case 'u':
				if p.pos+4 > len(p.src) {
					return "", p.errAt(p.pos-2, `invalid \u escape`)
				}
				n, err := strconv.ParseUint(p.src[p.pos:p.pos+4], 16, 32)
				if err != nil {
					return "", p.errAt(p.pos-2, `invalid \u escape`)
				}
				b.WriteRune(rune(n))
				p.pos += 4
			default: // \\ \' \" \/ and any other character stand for themselves
				b.WriteByte(esc)
			}
		default:
			b.WriteByte(ch)
			p.pos++
		}
	}
	return "", p.errAt(start, "unterminated string")
}

func (p *parser) number() (any, error) {
	start := p.pos
	if p.peek() == '-' {
		p.pos++
	}
	isFloat := false
	for p.pos < len(p.src) {
		ch := p.src[p.pos]
		switch {
		case ch >= '0' && ch <= '9':
		case ch == '.' || ch == 'e' || ch == 'E':
			isFloat = true
		case (ch == '+' || ch == '-') && (p.src[p.pos-1] == 'e' || p.src[p.pos-1] == 'E'):
		default:
			goto done
		}
		p.pos++
	}
done:
	text := p.src[start:p.pos]
	if !isFloat {
		if n, err := strconv.ParseInt(text, 10, 64); err == nil {
			if n >= math.MinInt32 && n <= math.MaxInt32 {
				return int32(n), nil
			}
			return n, nil
		}
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil || text == "-" {
		return nil, p.errAt(start, fmt.Sprintf("invalid number %q", text))
	}
	return f, nil
}

func (p *parser) regex() (bson.Regex, error) {
	start := p.pos
	p.pos++ // /
	inClass := false
	var b strings.Builder
	for {
		if p.pos >= len(p.src) || p.src[p.pos] == '\n' {
			return bson.Regex{}, p.errAt(start, "unterminated regex")
		}
		ch := p.src[p.pos]
		if ch == '\\' && p.pos+1 < len(p.src) {
			b.WriteString(p.src[p.pos : p.pos+2])
			p.pos += 2
			continue
		}
		p.pos++
		if ch == '/' && !inClass {
			break
		}
		if ch == '[' {
			inClass = true
		} else if ch == ']' {
			inClass = false
		}
		b.WriteByte(ch)
	}
	if strings.ContainsRune(b.String(), 0) {
		return bson.Regex{}, p.errAt(start, "a regex cannot contain a NUL character")
	}
	flagsAt := p.pos
	for p.pos < len(p.src) && isIdentPart(p.src[p.pos]) {
		p.pos++
	}
	flags := p.src[flagsAt:p.pos]
	for _, f := range flags {
		if !strings.ContainsRune("imsxu", f) {
			return bson.Regex{}, p.errAt(flagsAt, fmt.Sprintf("unsupported regex flag %q (MongoDB accepts i, m, s, x, u)", string(f)))
		}
	}
	return bson.Regex{Pattern: b.String(), Options: sortFlags(flags)}, nil
}

// sortFlags orders regex flags alphabetically, as BSON requires.
func sortFlags(f string) string {
	out := []byte(f)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return string(out)
}

// word2value parses a bare word in value position: a literal or a helper call.
func (p *parser) word2value() (any, error) {
	at := p.pos
	w, err := p.ident()
	if err != nil {
		return nil, err
	}
	switch w {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null", "undefined":
		return nil, nil
	case "new":
		p.skip()
		at = p.pos
		if w, err = p.ident(); err != nil {
			return nil, err
		}
		if w != "Date" && w != "ObjectId" && w != "NumberDecimal" && w != "NumberLong" && w != "NumberInt" && w != "UUID" && w != "Timestamp" {
			return nil, p.errAt(at, fmt.Sprintf("unsupported expression \"new %s\"", w))
		}
	}
	p.skip()
	if p.peek() != '(' {
		return nil, p.errAt(at, fmt.Sprintf("unsupported expression %q (only literals and ObjectId(), ISODate(), NumberLong() and similar helpers are allowed)", w))
	}
	args, err := p.args()
	if err != nil {
		return nil, err
	}
	return p.helper(at, w, args)
}

func (p *parser) helper(at int, name string, args []any) (any, error) {
	str := func() (string, bool) {
		if len(args) != 1 {
			return "", false
		}
		s, ok := args[0].(string)
		return s, ok
	}
	switch name {
	case "ObjectId":
		s, ok := str()
		if !ok {
			return nil, p.errAt(at, "ObjectId() needs a 24-character hex string")
		}
		id, err := bson.ObjectIDFromHex(s)
		if err != nil {
			return nil, p.errAt(at, fmt.Sprintf("invalid ObjectId %q", s))
		}
		return id, nil
	case "ISODate", "Date":
		if len(args) == 0 {
			return nil, p.errAt(at, "new Date() needs an argument (qh does not evaluate the current time)")
		}
		if len(args) == 1 {
			switch v := args[0].(type) {
			case string:
				t, err := parseDate(v)
				if err != nil {
					return nil, p.errAt(at, fmt.Sprintf("invalid date %q", v))
				}
				return bson.NewDateTimeFromTime(t), nil
			case int32:
				return bson.DateTime(int64(v)), nil
			case int64:
				return bson.DateTime(v), nil
			case float64:
				return bson.DateTime(int64(v)), nil
			}
		}
		return nil, p.errAt(at, name+"() takes one date string or a number of milliseconds")
	case "NumberInt", "NumberLong":
		var n int64
		var err error
		switch v := one(args).(type) {
		case int32:
			n = int64(v)
		case int64:
			n = v
		case string:
			n, err = strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		default:
			err = fmt.Errorf("bad")
		}
		if err != nil || len(args) != 1 {
			return nil, p.errAt(at, name+"() takes one integer")
		}
		if name == "NumberInt" {
			if n < math.MinInt32 || n > math.MaxInt32 {
				return nil, p.errAt(at, "NumberInt() is out of range")
			}
			return int32(n), nil
		}
		return n, nil
	case "NumberDecimal":
		var text string
		switch v := one(args).(type) {
		case string:
			text = v
		case int32, int64, float64:
			text = fmt.Sprint(v)
		}
		d, err := bson.ParseDecimal128(text)
		if err != nil || len(args) != 1 {
			return nil, p.errAt(at, "NumberDecimal() takes one number as a string")
		}
		return d, nil
	case "UUID":
		s, ok := str()
		raw, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
		if !ok || err != nil || len(raw) != 16 {
			return nil, p.errAt(at, "UUID() takes one UUID string")
		}
		return bson.Binary{Subtype: 4, Data: raw}, nil
	case "Timestamp":
		if len(args) == 2 {
			t, ok1 := asUint32(args[0])
			i, ok2 := asUint32(args[1])
			if ok1 && ok2 {
				return bson.Timestamp{T: t, I: i}, nil
			}
		}
		return nil, p.errAt(at, "Timestamp() takes two integers (seconds, increment)")
	case "MinKey", "MaxKey":
		if len(args) != 0 {
			return nil, p.errAt(at, name+"() takes no arguments")
		}
		if name == "MinKey" {
			return bson.MinKey{}, nil
		}
		return bson.MaxKey{}, nil
	}
	return nil, p.errAt(at, fmt.Sprintf("unsupported expression %q (only literals and ObjectId(), ISODate(), NumberLong() and similar helpers are allowed)", name))
}

func one(args []any) any {
	if len(args) != 1 {
		return nil
	}
	return args[0]
}

func asUint32(v any) (uint32, bool) {
	var n int64
	switch x := v.(type) {
	case int32:
		n = int64(x)
	case int64:
		n = x
	default:
		return 0, false
	}
	if n < 0 || n > math.MaxUint32 {
		return 0, false
	}
	return uint32(n), true
}

var dateLayouts = []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05Z0700",
	"2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02"}

// parseDate accepts the forms mongosh's ISODate accepts; times without a
// zone are UTC.
func parseDate(s string) (time.Time, error) {
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("bad date")
}

func (p *parser) ident() (string, error) {
	start := p.pos
	if p.pos >= len(p.src) || !isIdentStart(p.src[p.pos]) {
		return "", p.unexpected()
	}
	for p.pos < len(p.src) && isIdentPart(p.src[p.pos]) {
		p.pos++
	}
	return p.src[start:p.pos], nil
}

// keyWord is an unquoted object key: an identifier, or digits.
func (p *parser) keyWord() (string, error) {
	start := p.pos
	for p.pos < len(p.src) && isIdentPart(p.src[p.pos]) {
		p.pos++
	}
	if p.pos == start {
		return "", p.unexpected()
	}
	return p.src[start:p.pos], nil
}

// word consumes w if the input continues with it as a whole word.
func (p *parser) word(w string) bool {
	if strings.HasPrefix(p.src[p.pos:], w) && (p.pos+len(w) == len(p.src) || !isIdentPart(p.src[p.pos+len(w)])) {
		p.pos += len(w)
		return true
	}
	return false
}

func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isIdentPart(c byte) bool { return isIdentStart(c) || c >= '0' && c <= '9' }

func (p *parser) peek() byte {
	if p.pos >= len(p.src) {
		return 0
	}
	return p.src[p.pos]
}

func (p *parser) expect(c byte) error {
	p.skip()
	if p.peek() != c {
		return p.unexpected()
	}
	p.pos++
	return nil
}

// skip skips whitespace and comments.
func (p *parser) skip() {
	for p.pos < len(p.src) {
		switch {
		case strings.ContainsRune(" \t\r\n", rune(p.src[p.pos])):
			p.pos++
		case strings.HasPrefix(p.src[p.pos:], "//"):
			if i := strings.IndexByte(p.src[p.pos:], '\n'); i >= 0 {
				p.pos += i + 1
			} else {
				p.pos = len(p.src)
			}
		case strings.HasPrefix(p.src[p.pos:], "/*"):
			if i := strings.Index(p.src[p.pos+2:], "*/"); i >= 0 {
				p.pos += i + 4
			} else {
				p.pos = len(p.src)
			}
		default:
			return
		}
	}
}

func (p *parser) unexpected() error {
	if p.pos >= len(p.src) {
		return p.errAt(p.pos, "unexpected end of query")
	}
	r, _ := utf8.DecodeRuneInString(p.src[p.pos:])
	return p.errAt(p.pos, fmt.Sprintf("unexpected %q", string(r)))
}

// errAt reports msg at byte offset pos as "msg at line:col", with the query
// line and a caret in the hint.
func (p *parser) errAt(pos int, msg string) error {
	pos = min(pos, len(p.src))
	lineStart := strings.LastIndexByte(p.src[:pos], '\n') + 1
	lineEnd := strings.IndexByte(p.src[pos:], '\n')
	if lineEnd < 0 {
		lineEnd = len(p.src)
	} else {
		lineEnd += pos
	}
	line := strings.Count(p.src[:pos], "\n") + 1
	col := utf8.RuneCountInString(p.src[lineStart:pos]) + 1
	text := p.src[lineStart:lineEnd]
	if len(text) > 200 { // keep the hint short around the position
		from := max(0, pos-lineStart-100)
		text = text[from:min(len(text), from+200)]
		col -= utf8.RuneCountInString(p.src[lineStart : lineStart+from])
	}
	e := qerr.New(qerr.Invalid, fmt.Sprintf("%s at %d:%d", msg, line, col))
	return e.WithHint(text + "\n" + strings.Repeat(" ", col-1) + "^")
}
