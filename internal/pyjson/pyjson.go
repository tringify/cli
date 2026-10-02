// Package pyjson reads and writes JSON the way Python's json module does.
//
// Theme files were written by the original Python theme tools, and authors
// keep them in version control. Rewriting theme.json or a compiled section
// must therefore produce byte-for-byte what json.dumps produced: object keys
// keep their order, integers keep every digit, floats use Python's repr, and
// non-ASCII text is escaped unless asked otherwise. Parse errors carry the
// same messages and positions, so a theme author sees the same diagnostics.
package pyjson

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Value is one of: nil, bool, Int, Float, String, *Object or []Value.
type Value any

// Int is an integer of any size, kept as its normalised decimal digits.
type Int string

// Float is a JSON number with a fraction or exponent, or NaN/Infinity.
type Float float64

// String holds text. Lone surrogates from \uD800-style escapes are stored
// in WTF-8 so they survive a round trip, as they do in Python.
type String string

// Object is a JSON object that remembers key order. A repeated key keeps
// its first position and its last value, like a Python dict.
type Object struct {
	keys   []string
	values map[string]Value
}

// NewObject returns an empty object.
func NewObject() *Object { return &Object{values: map[string]Value{}} }

// Keys returns the keys in order.
func (o *Object) Keys() []string { return append([]string(nil), o.keys...) }

// Len returns the number of keys.
func (o *Object) Len() int { return len(o.keys) }

// Get returns the value for key.
func (o *Object) Get(key string) (Value, bool) {
	v, ok := o.values[key]
	return v, ok
}

// Set adds or replaces key, keeping an existing key's position.
func (o *Object) Set(key string, value Value) {
	if _, ok := o.values[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

// Delete removes key if present.
func (o *Object) Delete(key string) {
	if _, ok := o.values[key]; !ok {
		return
	}
	delete(o.values, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// Copy returns a shallow copy.
func (o *Object) Copy() *Object {
	c := NewObject()
	for _, k := range o.keys {
		c.Set(k, o.values[k])
	}
	return c
}

// DecodeError matches Python's json.JSONDecodeError.
type DecodeError struct {
	Msg    string
	Pos    int // in code points
	Line   int
	Column int
}

func (e *DecodeError) Error() string {
	return fmt.Sprintf("%s: line %d column %d (char %d)", e.Msg, e.Line, e.Column, e.Pos)
}

// NormalizeNewlines applies Python's universal newline translation, which
// Path.read_text performs before the JSON parser sees the text.
func NormalizeNewlines(s string) string {
	if !strings.Contains(s, "\r") {
		return s
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// Loads parses s like json.loads.
func Loads(s string) (Value, error) {
	d := &decoder{s: []rune(s)}
	if len(d.s) > 0 && d.s[0] == '\ufeff' {
		return nil, d.err("Unexpected UTF-8 BOM (decode using utf-8-sig)", 0)
	}
	idx := d.ws(0)
	v, end, err := d.value(idx)
	if err != nil {
		return nil, err
	}
	end = d.ws(end)
	if end != len(d.s) {
		return nil, d.err("Extra data", end)
	}
	return v, nil
}

type decoder struct{ s []rune }

type stop struct{ idx int }

func (stop) Error() string { return "stop" }

func (d *decoder) err(msg string, pos int) *DecodeError {
	line, last := 1, -1
	for i := 0; i < pos && i < len(d.s); i++ {
		if d.s[i] == '\n' {
			line++
			last = i
		}
	}
	return &DecodeError{Msg: msg, Pos: pos, Line: line, Column: pos - last}
}

func isWS(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }

func (d *decoder) ws(i int) int {
	for i < len(d.s) && isWS(d.s[i]) {
		i++
	}
	return i
}

func (d *decoder) has(i int, word string) bool {
	w := []rune(word)
	if i+len(w) > len(d.s) {
		return false
	}
	for j, r := range w {
		if d.s[i+j] != r {
			return false
		}
	}
	return true
}

// value scans one value starting exactly at i. A position where no value
// starts is reported as "Expecting value" there, as Python's scanner does.
func (d *decoder) value(i int) (Value, int, error) {
	v, end, err := d.scan(i)
	if s, ok := err.(stop); ok {
		return nil, 0, d.err("Expecting value", s.idx)
	}
	return v, end, err
}

func (d *decoder) scan(i int) (Value, int, error) {
	if i >= len(d.s) {
		return nil, 0, stop{i}
	}
	switch c := d.s[i]; {
	case c == '"':
		return d.str(i + 1)
	case c == '{':
		return d.object(i + 1)
	case c == '[':
		return d.array(i + 1)
	case c == 'n' && d.has(i, "null"):
		return nil, i + 4, nil
	case c == 't' && d.has(i, "true"):
		return true, i + 4, nil
	case c == 'f' && d.has(i, "false"):
		return false, i + 5, nil
	case c == 'N' && d.has(i, "NaN"):
		return Float(math.NaN()), i + 3, nil
	case c == 'I' && d.has(i, "Infinity"):
		return Float(math.Inf(1)), i + 8, nil
	case c == '-' && d.has(i, "-Infinity"):
		return Float(math.Inf(-1)), i + 9, nil
	}
	return d.number(i)
}

func digit(r rune) bool { return r >= '0' && r <= '9' }

func (d *decoder) number(start int) (Value, int, error) {
	i, n := start, len(d.s)
	if i < n && d.s[i] == '-' {
		i++
	}
	if i >= n || !digit(d.s[i]) {
		return nil, 0, stop{start}
	}
	if d.s[i] == '0' {
		i++
	} else {
		for i < n && digit(d.s[i]) {
			i++
		}
	}
	isFloat := false
	if i+1 < n && d.s[i] == '.' && digit(d.s[i+1]) {
		isFloat = true
		i += 2
		for i < n && digit(d.s[i]) {
			i++
		}
	}
	if i < n && (d.s[i] == 'e' || d.s[i] == 'E') {
		j := i + 1
		if j < n && (d.s[j] == '+' || d.s[j] == '-') {
			j++
		}
		if j < n && digit(d.s[j]) {
			for j < n && digit(d.s[j]) {
				j++
			}
			isFloat = true
			i = j
		}
	}
	text := string(d.s[start:i])
	if isFloat {
		f, err := strconv.ParseFloat(text, 64)
		if err != nil && !math.IsInf(f, 0) {
			return nil, 0, d.err("Expecting value", start)
		}
		return Float(f), i, nil
	}
	b, ok := new(big.Int).SetString(text, 10)
	if !ok {
		return nil, 0, d.err("Expecting value", start)
	}
	return Int(b.String()), i, nil
}

// str scans a string whose opening quote is at i-1.
func (d *decoder) str(i int) (Value, int, error) {
	begin := i - 1
	var b strings.Builder
	n := len(d.s)
	end := i
	for {
		next := end
		for next < n {
			c := d.s[next]
			if c == '"' || c == '\\' {
				break
			}
			if c <= 0x1f {
				return nil, 0, d.err("Invalid control character at", next)
			}
			next++
		}
		if next >= n {
			return nil, 0, d.err("Unterminated string starting at", begin)
		}
		b.WriteString(string(d.s[end:next]))
		if d.s[next] == '"' {
			return String(b.String()), next + 1, nil
		}
		next++ // past the backslash
		if next == n {
			return nil, 0, d.err("Unterminated string starting at", begin)
		}
		c := d.s[next]
		if c != 'u' {
			end = next + 1
			switch c {
			case '"', '\\', '/':
				b.WriteRune(c)
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			default:
				return nil, 0, d.err("Invalid \\escape", end-2)
			}
			continue
		}
		next++
		end = next + 4
		if end >= n {
			return nil, 0, d.err("Invalid \\uXXXX escape", next-1)
		}
		cp, ok := hex4(d.s[next:end])
		if !ok {
			return nil, 0, d.err("Invalid \\uXXXX escape", end-5)
		}
		if cp >= 0xd800 && cp <= 0xdbff && end+6 < n && d.s[end] == '\\' && d.s[end+1] == 'u' {
			low, ok := hex4(d.s[end+2 : end+6])
			if !ok {
				return nil, 0, d.err("Invalid \\uXXXX escape", end+1)
			}
			if low >= 0xdc00 && low <= 0xdfff {
				cp = 0x10000 + (cp-0xd800)<<10 + (low - 0xdc00)
				end += 6
			}
		}
		writeCodePoint(&b, cp)
	}
}

func hex4(r []rune) (rune, bool) {
	var v rune
	for _, c := range r {
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v |= c - '0'
		case c >= 'a' && c <= 'f':
			v |= c - 'a' + 10
		case c >= 'A' && c <= 'F':
			v |= c - 'A' + 10
		default:
			return 0, false
		}
	}
	return v, true
}

// writeCodePoint writes cp, encoding a lone surrogate as WTF-8.
func writeCodePoint(b *strings.Builder, cp rune) {
	if cp >= 0xd800 && cp <= 0xdfff {
		b.WriteByte(byte(0xe0 | cp>>12))
		b.WriteByte(byte(0x80 | (cp>>6)&0x3f))
		b.WriteByte(byte(0x80 | cp&0x3f))
		return
	}
	b.WriteRune(cp)
}

// codePoints decodes s, including WTF-8 lone surrogates.
func codePoints(s string) []rune {
	out := make([]rune, 0, len(s))
	for i := 0; i < len(s); {
		if s[i] == 0xed && i+2 < len(s) && s[i+1] >= 0xa0 && s[i+1] <= 0xbf {
			out = append(out, rune(s[i]&0x0f)<<12|rune(s[i+1]&0x3f)<<6|rune(s[i+2]&0x3f))
			i += 3
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		out = append(out, r)
		i += size
	}
	return out
}

func (d *decoder) object(i int) (Value, int, error) {
	obj := NewObject()
	n := len(d.s)
	i = d.ws(i)
	if i >= n || d.s[i] != '}' {
		for {
			if i >= n || d.s[i] != '"' {
				return nil, 0, d.err("Expecting property name enclosed in double quotes", i)
			}
			key, next, err := d.str(i + 1)
			if err != nil {
				return nil, 0, err
			}
			i = d.ws(next)
			if i >= n || d.s[i] != ':' {
				return nil, 0, d.err("Expecting ':' delimiter", i)
			}
			i = d.ws(i + 1)
			v, next, err := d.value(i)
			if err != nil {
				return nil, 0, err
			}
			obj.Set(string(key.(String)), v)
			i = d.ws(next)
			if i < n && d.s[i] == '}' {
				break
			}
			if i >= n || d.s[i] != ',' {
				return nil, 0, d.err("Expecting ',' delimiter", i)
			}
			i = d.ws(i + 1)
		}
	}
	return obj, i + 1, nil
}

func (d *decoder) array(i int) (Value, int, error) {
	list := []Value{}
	n := len(d.s)
	i = d.ws(i)
	if i >= n || d.s[i] != ']' {
		for {
			v, next, err := d.value(i)
			if err != nil {
				return nil, 0, err
			}
			list = append(list, v)
			i = d.ws(next)
			if i < n && d.s[i] == ']' {
				break
			}
			if i >= n || d.s[i] != ',' {
				return nil, 0, d.err("Expecting ',' delimiter", i)
			}
			i = d.ws(i + 1)
		}
	}
	return list, i + 1, nil
}

// Options control Dumps. The zero value is json.dumps(value).
type Options struct {
	// Indent of 0 means no indentation (compact separators ", " and ": ").
	Indent int
	// ASCII escapes every character outside printable ASCII, as
	// ensure_ascii=True (Python's default) does. Use DumpsUnicode for
	// ensure_ascii=False.
	ASCII bool
}

// Dumps is json.dumps(v, indent=2) with ensure_ascii=True.
func Dumps(v Value) string { return Encode(v, Options{Indent: 2, ASCII: true}) }

// DumpsUnicode is json.dumps(v, indent=2, ensure_ascii=False).
func DumpsUnicode(v Value) string { return Encode(v, Options{Indent: 2}) }

// Compact is json.dumps(v): ", " and ": " separators, ASCII escaping.
func Compact(v Value) string { return Encode(v, Options{ASCII: true}) }

// Encode writes v as json.dumps would.
func Encode(v Value, o Options) string {
	var b strings.Builder
	encode(&b, v, o, 0)
	return b.String()
}

func encode(b *strings.Builder, v Value, o Options, level int) {
	newline := func(l int) {
		if o.Indent > 0 {
			b.WriteByte('\n')
			b.WriteString(strings.Repeat(" ", o.Indent*l))
		}
	}
	sep := ", "
	if o.Indent > 0 {
		sep = ","
	}
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case Int:
		b.WriteString(string(t))
	case Float:
		b.WriteString(FloatJSON(float64(t)))
	case String:
		b.WriteString(Quote(string(t), o.ASCII))
	case string:
		b.WriteString(Quote(t, o.ASCII))
	case []Value:
		if len(t) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteByte('[')
		for i, item := range t {
			if i > 0 {
				b.WriteString(sep)
			}
			newline(level + 1)
			encode(b, item, o, level+1)
		}
		newline(level)
		b.WriteByte(']')
	case *Object:
		if t.Len() == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteByte('{')
		for i, k := range t.keys {
			if i > 0 {
				b.WriteString(sep)
			}
			newline(level + 1)
			b.WriteString(Quote(k, o.ASCII))
			b.WriteString(": ")
			encode(b, t.values[k], o, level+1)
		}
		newline(level)
		b.WriteByte('}')
	default:
		panic(fmt.Sprintf("pyjson: unsupported value %T", v))
	}
}

// Quote encodes s as a JSON string literal the way Python does.
func Quote(s string, ascii bool) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range codePoints(s) {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			switch {
			case r < 0x20:
				fmt.Fprintf(&b, `\u%04x`, r)
			case ascii && r > 0x7e:
				if r > 0xffff {
					r -= 0x10000
					fmt.Fprintf(&b, `\u%04x\u%04x`, 0xd800|(r>>10)&0x3ff, 0xdc00|r&0x3ff)
				} else {
					fmt.Fprintf(&b, `\u%04x`, r)
				}
			case r >= 0xd800 && r <= 0xdfff:
				b.WriteByte(byte(0xe0 | r>>12))
				b.WriteByte(byte(0x80 | (r>>6)&0x3f))
				b.WriteByte(byte(0x80 | r&0x3f))
			default:
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// FloatJSON is how json.dumps writes a float.
func FloatJSON(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	return FloatRepr(f)
}

// FloatRepr is Python's repr(float): the shortest round-trip digits, in
// positional notation for exponents from -5 to 15 and with ".0" for whole
// numbers, otherwise as d.ddde+XX.
func FloatRepr(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	e := strconv.FormatFloat(f, 'e', -1, 64) // -d.dddde±XX
	sign := ""
	if e[0] == '-' {
		sign, e = "-", e[1:]
	}
	mant, expText, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	exp, _ := strconv.Atoi(expText)
	if exp < -4 || exp >= 16 {
		m := digits[:1]
		if len(digits) > 1 {
			m += "." + digits[1:]
		}
		es := "+"
		if exp < 0 {
			es, exp = "-", -exp
		}
		return fmt.Sprintf("%s%se%s%02d", sign, m, es, exp)
	}
	switch {
	case exp >= len(digits)-1:
		return sign + digits + strings.Repeat("0", exp-(len(digits)-1)) + ".0"
	case exp >= 0:
		return sign + digits[:exp+1] + "." + digits[exp+1:]
	default:
		return sign + "0." + strings.Repeat("0", -exp-1) + digits
	}
}

// Truthy is Python truthiness for a JSON value.
func Truthy(v Value) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case Int:
		return t != "0"
	case Float:
		return t != 0
	case String:
		return t != ""
	case string:
		return t != ""
	case []Value:
		return len(t) > 0
	case *Object:
		return t.Len() > 0
	}
	return true
}

// numeric returns a number's value, treating booleans as 0 and 1 the way
// Python's == does.
func numeric(v Value) (*big.Float, bool) {
	switch t := v.(type) {
	case bool:
		if t {
			return big.NewFloat(1), true
		}
		return big.NewFloat(0), true
	case Int:
		f, _, err := big.ParseFloat(string(t), 10, 4096, big.ToNearestEven)
		return f, err == nil
	case Float:
		if math.IsNaN(float64(t)) || math.IsInf(float64(t), 0) {
			return nil, false
		}
		return big.NewFloat(float64(t)), true
	}
	return nil, false
}

// Equal is Python == for JSON values (so 3 == 3.0 == 3 and True == 1).
func Equal(a, b Value) bool {
	if x, ok := numeric(a); ok {
		if y, ok := numeric(b); ok {
			return x.Cmp(y) == 0
		}
		return false
	}
	if fa, ok := a.(Float); ok {
		if fb, ok := b.(Float); ok {
			return float64(fa) == float64(fb)
		}
		return false
	}
	switch x := a.(type) {
	case nil:
		return b == nil
	case String:
		y, ok := b.(String)
		return ok && x == y
	case []Value:
		y, ok := b.([]Value)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !Equal(x[i], y[i]) {
				return false
			}
		}
		return true
	case *Object:
		y, ok := b.(*Object)
		if !ok || x.Len() != y.Len() {
			return false
		}
		for _, k := range x.keys {
			yv, ok := y.values[k]
			if !ok || !Equal(x.values[k], yv) {
				return false
			}
		}
		return true
	}
	return false
}

// IsInt reports whether v == n in Python.
func IsInt(v Value, n int64) bool { return Equal(v, Int(strconv.FormatInt(n, 10))) }

// Str is Python's str() of a decoded JSON value.
func Str(v Value) string {
	if s, ok := v.(String); ok {
		return string(s)
	}
	return Repr(v)
}

// Repr is Python's repr() of a decoded JSON value.
func Repr(v Value) string {
	switch t := v.(type) {
	case nil:
		return "None"
	case bool:
		if t {
			return "True"
		}
		return "False"
	case Int:
		return string(t)
	case Float:
		return FloatRepr(float64(t))
	case String:
		return reprString(string(t))
	case []Value:
		parts := make([]string, len(t))
		for i, item := range t {
			parts[i] = Repr(item)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *Object:
		parts := make([]string, 0, t.Len())
		for _, k := range t.keys {
			parts = append(parts, reprString(k)+": "+Repr(t.values[k]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return fmt.Sprint(v)
}

func reprString(s string) string {
	quote := '\''
	if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
		quote = '"'
	}
	var b strings.Builder
	b.WriteRune(quote)
	for _, r := range codePoints(s) {
		switch {
		case r == quote || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r >= 0xd800 && r <= 0xdfff:
			fmt.Fprintf(&b, `\u%04x`, r)
		case r < 0x7f:
			b.WriteRune(r)
		case !strconv.IsPrint(r):
			switch {
			case r <= 0xff:
				fmt.Fprintf(&b, `\x%02x`, r)
			case r <= 0xffff:
				fmt.Fprintf(&b, `\u%04x`, r)
			default:
				fmt.Fprintf(&b, `\U%08x`, r)
			}
		default:
			b.WriteRune(r)
		}
	}
	b.WriteRune(quote)
	return b.String()
}
