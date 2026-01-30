// Copyright © 2026, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: BSD-3-Clause

package xtract

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/sassoftware/pdf-xtract/logger"
)

// A Page represent a single page in a PDF file.
// The methods interpret a Page dictionary stored in V.
type Page struct {
	V Value
}

// Page returns the page for the given page number.
// Page numbers are indexed starting at 1, not 0.
// If the page is not found, Page returns a Page with p.V.IsNull().
func (r *Reader) Page(num int) Page {
	logger.Debug(fmt.Sprintf("Reading Page %d", num), true)
	num-- // now 0-indexed
	page := r.Trailer().Key("Root").Key("Pages")
Search:
	for page.Key("Type").Name() == "Pages" {
		count := int(page.Key("Count").Int64())
		if count < num {
			return Page{}
		}
		kids := page.Key("Kids")
		logger.Debug(fmt.Sprintf("count of pages: %d, kids: %d", count, kids.Int64()))
		for i := 0; i < kids.Len(); i++ {
			kid := kids.Index(i)
			if kid.Key("Type").Name() == "Pages" {
				c := int(kid.Key("Count").Int64())
				if num < c {
					page = kid
					continue Search
				}
				num -= c
				continue
			}
			if kid.Key("Type").Name() == "Page" {
				if num == 0 {
					return Page{kid}
				}
				num--
			}
		}
		break
	}
	return Page{}
}

// NumPage returns the number of pages in the PDF file.
func (r *Reader) NumPage() int {
	return int(r.Trailer().Key("Root").Key("Pages").Key("Count").Int64())
}

// GetPlainText returns all the text in the PDF file
func (r *Reader) GetPlainText() (reader io.Reader, err error) {
	pages := r.NumPage()
	logger.Debug(fmt.Sprintf("total pages = %d", pages), true)
	var buf bytes.Buffer
	fonts := make(map[string]*Font)
	for i := 1; i <= pages; i++ {
		p := r.Page(i)
		logger.Debug(fmt.Sprintf("/Page %d %d R", p.V.ptr.id, p.V.ptr.gen), true)
		for _, name := range p.Fonts() { // cache fonts so we don't continually parse charmap
			if _, ok := fonts[name]; !ok {
				f := p.Font(name)
				logger.Debug(fmt.Sprintf("/Font %d %d R", f.V.ptr.id, f.V.ptr.gen), true)

				fonts[name] = &f
			}
		}
		text, err := p.GetPlainText(fonts)
		if err != nil {
			return &bytes.Buffer{}, err
		}
		buf.WriteString(text)
	}
	logger.Debug("Successfully completed parsing", true)

	return &buf, nil
}

// GetStyledTexts returns list all sentences in an array, that are included styles
func (r *Reader) GetStyledTexts() (sentences []Text, err error) {
	totalPage := r.NumPage()
	for pageIndex := 1; pageIndex <= totalPage; pageIndex++ {
		p := r.Page(pageIndex)

		if p.V.IsNull() || p.V.Key("Contents").Kind() == Null {
			continue
		}
		var lastTextStyle Text
		texts := p.Content().Text
		for _, text := range texts {
			if lastTextStyle == (Text{}) {
				lastTextStyle = text
				continue
			}

			if IsSameSentence(lastTextStyle, text) {
				lastTextStyle.S = lastTextStyle.S + text.S
			} else {
				sentences = append(sentences, lastTextStyle)
				lastTextStyle = text
			}
		}
		if len(lastTextStyle.S) > 0 {
			sentences = append(sentences, lastTextStyle)
		}
	}

	return sentences, err
}

func (p Page) findInherited(key string) Value {
	logger.Debug("inside findInherited")
	for v := p.V; !v.IsNull(); v = v.Key("Parent") {
		if r := v.Key(key); !r.IsNull() {
			logger.Debug(fmt.Sprintf("findInherited: found key %q in object %d %d R", key, v.ptr.id, v.ptr.gen))
			return r
		}
	}
	return Value{}
}

/*
func (p Page) MediaBox() Value {
	return p.findInherited("MediaBox")
}

func (p Page) CropBox() Value {
	return p.findInherited("CropBox")
}
*/

// Resources returns the resources dictionary associated with the page.
func (p Page) Resources() Value {
	logger.Debug(fmt.Sprintf("Resources: fetching /Resources for Page %d %d R", p.V.ptr.id, p.V.ptr.gen))
	return p.findInherited("Resources")
}

// Fonts returns a list of the fonts associated with the page.
func (p Page) Fonts() []string {
	logger.Debug(fmt.Sprintf("Fonts: retrieving /Font list for Page %d %d R", p.V.ptr.id, p.V.ptr.gen))
	return p.Resources().Key("Font").Keys()
}

// Font returns the font with the given name associated with the page.
func (p Page) Font(name string) Font {
	return Font{p.Resources().Key("Font").Key(name), nil}
}

// A Font represent a font in a PDF file.
// The methods interpret a Font dictionary stored in V.
type Font struct {
	V   Value
	enc TextEncoding
}

// BaseFont returns the font's name (BaseFont property).
func (f Font) BaseFont() string {
	return f.V.Key("BaseFont").Name()
}

// FirstChar returns the code point of the first character in the font.
func (f Font) FirstChar() int {
	return int(f.V.Key("FirstChar").Int64())
}

// LastChar returns the code point of the last character in the font.
func (f Font) LastChar() int {
	return int(f.V.Key("LastChar").Int64())
}

// Widths returns the widths of the glyphs in the font.
// In a well-formed PDF, len(f.Widths()) == f.LastChar()+1 - f.FirstChar().
func (f Font) Widths() []float64 {
	x := f.V.Key("Widths")
	var out []float64
	for i := 0; i < x.Len(); i++ {
		out = append(out, x.Index(i).Float64())
	}
	logger.Debug(fmt.Sprintf("Widths: extracted %d glyph widths for Font %d %d R", len(out), f.V.ptr.id, f.V.ptr.gen), true)
	return out
}

// Width returns the width of the given code point.
func (f Font) Width(code int) float64 {
	first := f.FirstChar()
	last := f.LastChar()
	if code < first || last < code {
		return 0
	}
	return f.V.Key("Widths").Index(code - first).Float64()
}

// Encoder returns the encoding between font code point sequences and UTF-8.
func (f Font) Encoder() TextEncoding {
	logger.Debug("retrieving text encoding")
	if f.enc == nil { // caching the Encoder so we don't have to continually parse charmap
		f.enc = f.getEncoder()
	}
	return f.enc
}

func (f Font) getEncoder() TextEncoding {
	logger.Debug(fmt.Sprintf("getEncoder: determining text encoding for Font %d %d R", f.V.ptr.id, f.V.ptr.gen))
	enc := f.V.Key("Encoding")
	switch enc.Kind() {
	case Name:
		logger.Debug(fmt.Sprintf("getEncoder: found named encoding = %q", enc.Name()), true)
		switch enc.Name() {
		case "WinAnsiEncoding":
			return &byteEncoder{&winAnsiEncoding}
		case "MacRomanEncoding":
			return &byteEncoder{&macRomanEncoding}
		case "Identity-H":
			return f.charmapEncoding()
		default:
			logger.Debug("unknown encoding : %d", enc.Name())
			return &nopEncoder{}
		}
	case Dict:
		return &dictEncoder{enc.Key("Differences")}
	case Null:
		return f.charmapEncoding()
	default:
		logger.Debug("unexpected encoding : %d", enc.String())

		return &nopEncoder{}
	}
}

func (f *Font) charmapEncoding() TextEncoding {
	toUnicode := f.V.Key("ToUnicode")
	if toUnicode.Kind() == Stream {
		logger.Debug("charmapEncoding: found ToUnicode stream — attempting to read CMap", true)
		m := readCmap(toUnicode)
		if m == nil {
			return &nopEncoder{}
		}
		return m
	}
	logger.Debug("charmapEncoding: no ToUnicode stream found — using pdfDocEncoding", true)
	return &byteEncoder{&pdfDocEncoding}
}

type dictEncoder struct {
	v Value
}

func (e *dictEncoder) Decode(raw string) (text string) {
	logger.Debug("decoding dictEncoding")
	r := make([]rune, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		ch := rune(raw[i])
		n := -1
		for j := 0; j < e.v.Len(); j++ {
			x := e.v.Index(j)
			if x.Kind() == Integer {
				n = int(x.Int64())
				continue
			}
			if x.Kind() == Name {
				if int(raw[i]) == n {
					r := nameToRune[x.Name()]
					if r != 0 {
						ch = r
						break
					}
				}
				n++
			}
		}
		r = append(r, ch)
	}
	return string(r)
}

// A TextEncoding represents a mapping between
// font code points and UTF-8 text.
type TextEncoding interface {
	// Decode returns the UTF-8 text corresponding to
	// the sequence of code points in raw.
	Decode(raw string) (text string)
}

type nopEncoder struct {
}

func (e *nopEncoder) Decode(raw string) (text string) {
	logger.Debug("decoding nopEncoder")
	return raw
}

type byteEncoder struct {
	table *[256]rune
}

func (e *byteEncoder) Decode(raw string) (text string) {
	logger.Debug("decoding byteEncoder")
	r := make([]rune, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		r = append(r, e.table[raw[i]])
	}
	return string(r)
}

type byteRange struct {
	low  string
	high string
}

type bfchar struct {
	orig string
	repl string
}

type bfrange struct {
	lo  string
	hi  string
	dst Value
}

type cmap struct {
	space   [4][]byteRange // codespace range
	bfrange []bfrange
	bfchar  []bfchar
}

// PDF CMaps define how encoded character codes map to Unicode values.
// There are three main mapping mechanisms:
//   • bfchar   – one-to-one explicit character mappings
//   • bfrange  – range-based mappings, which may map to strings or arrays
//   • fallback – when no mapping is found, the raw bytes may still represent
//                valid characters that should not be dropped
//
// Previous behavior :
//   • When no bfchar or bfrange mapping was found, the code appended a special
//     placeholder rune (noRune). This caused missing or garbled output when
//     encountering unmapped codes, since valid characters were silently replaced.
//
// Current behavior :
// mapped → UTF-16 decode, unmapped → UTF-8 or preserve, ensuring no silent data loss.
//
// Example cases:
//
//   // Explicit bfchar mapping
//   raw = "\x01"    // mapped in bfchar
//   → "A"
//
//   // Range mapping
//   raw = "\x05"    // falls in bfrange [\x05–\x10]
//   → "D"
//
//   // Unmapped but valid byte
//   raw = "\x7E"
//   → "~"   (instead of noRune)
//
//   // Unmapped invalid sequence
//   raw = "\xFF"
//   → decoded as rune 0xFF
//
// Summary of improvements:
// 1) Modularisation of the function
//    - New code factors the logic into small helpers:
//        • findNextCodespace(raw) → (code, width)
//        • resolveCodeMapping(code, width) → ([]rune, ok)
//
// 2) Correct, lossless fallbacks instead of sentinel runes
//    - Old code appended `noRune` whenever a code/range didn’t match or when no
//      codespace was found, effectively losing the original bytes and injecting
//      a placeholder. That corrupts text and makes debugging harder.
//    - New code uses DecodeUTF8OrPreserve(...) to *preserve the raw bytes* as a
//      valid UTF-8 rune when there is no explicit mapping. This keeps output
//      round-trippable and avoids data loss.
//
// 3) Explicit handling of “no codespace” vs “unmapped in codespace”
//    - Old code treated many error paths the same (append `noRune`), so callers
//      could not distinguish “byte not in any codespace” from “valid code but
//      unmapped”. New code:
//        • If no codespace matches: preserve the first byte and continue.
//        • If a codespace matches but no mapping exists: preserve the whole code.
//      This mirrors the PDF spec expectations and simplifies debugging.

// Decode translates raw character codes into Unicode runes using the CMap rules.
func (m *cmap) Decode(raw string) string {
	logger.Debug("decoding cmap")
	var runes []rune

	for len(raw) > 0 {
		//find next valid codespace match
		code, width := m.findNextCodespace(raw)
		if width == 0 {
			// no codespace, preserve first byte and continue
			runes = append(runes, DecodeUTF8OrPreserve(raw[:1])...)
			raw = raw[1:]
			continue
		}

		//Checking to resolve this code into a Unicode rune
		decoded, ok := m.resolveCodeMapping(code, width)
		if ok {
			runes = append(runes, decoded...)
		} else {
			// no explicit mapping then preserve raw bytes safely
			runes = append(runes, DecodeUTF8OrPreserve(code)...)
		}

		raw = raw[width:]
	}

	return string(runes)
}

// findNextCodespace checks raw for a valid codespace sequence of length 1–4.
// Returns the matched bytes and its length, or ("", 0) if no codespace matches.
func (m *cmap) findNextCodespace(raw string) (string, int) {
	for n := 1; n <= 4 && n <= len(raw); n++ {
		for _, space := range m.space[n-1] {
			if space.low <= raw[:n] && raw[:n] <= space.high {
				return raw[:n], n
			}
		}
	}
	return "", 0
}

// resolveCodeMapping tries to map a code using bfchar or bfrange rules.
// Returns decoded runes and true if a mapping was found.
func (m *cmap) resolveCodeMapping(code string, width int) ([]rune, bool) {
	// Exact bfchar match
	for _, bfchar := range m.bfchar {
		if len(bfchar.orig) == width && bfchar.orig == code {
			return []rune(utf16Decode(bfchar.repl)), true
		}
	}
	// bfrange match
	for _, br := range m.bfrange {
		if len(br.lo) == width && br.lo <= code && code <= br.hi {
			switch br.dst.Kind() {
			case String:
				return resolveBfrangeWithString(br, code), true
			case Array:
				return resolveBfrangeWithArray(br, code), true
			}
		}
	}

	return nil, false
}

// resolveBfrangeWithString handles bfrange mappings where dst is a String.
func resolveBfrangeWithString(br bfrange, code string) []rune {
	s := br.dst.RawString()
	if br.lo != code {
		// increment last byte according to offset within range
		b := []byte(s)
		b[len(b)-1] += code[len(code)-1] - br.lo[len(br.lo)-1]
		s = string(b)
	}
	return []rune(utf16Decode(s))
}

// resolveBfrangeWithArray handles bfrange mappings where dst is an Array.
func resolveBfrangeWithArray(br bfrange, code string) []rune {
	idx := code[len(code)-1] - br.lo[len(br.lo)-1]
	v := br.dst.Index(int(idx))
	if v.Kind() == String {
		return []rune(utf16Decode(v.RawString()))
	}
	return nil
}

func readCmap(toUnicode Value) *cmap {
	logger.Debug("reading Cmap")

	n := -1
	var m cmap
	ok := true
	Interpret(toUnicode, func(stk *Stack, op string) {
		if !ok {
			return
		}
		switch op {
		case "findresource":
			stk.Pop() // category
			stk.Pop() // key
			stk.Push(newDict())
		case "begincmap":
			stk.Push(newDict())
		case "endcmap":
			stk.Pop()
		case "begincodespacerange":
			n = int(stk.Pop().Int64())
		case "endcodespacerange":
			if n < 0 {
				logger.Debug("missing begincodespacerange")
				ok = false
				return
			}
			for i := 0; i < n; i++ {
				hi, lo := stk.Pop().RawString(), stk.Pop().RawString()
				if len(lo) == 0 || len(lo) != len(hi) {
					logger.Debug("bad codespace range")
					ok = false
					return
				}
				m.space[len(lo)-1] = append(m.space[len(lo)-1], byteRange{lo, hi})
			}
			n = -1
		case "beginbfchar":
			n = int(stk.Pop().Int64())
		case "endbfchar":
			if n < 0 {
				logger.Error("missing beginbfchar")
				panic("missing beginbfchar")
			}
			for i := 0; i < n; i++ {
				repl, orig := stk.Pop().RawString(), stk.Pop().RawString()
				m.bfchar = append(m.bfchar, bfchar{orig, repl})
			}
		case "beginbfrange":
			n = int(stk.Pop().Int64())
		case "endbfrange":
			if n < 0 {
				logger.Error("missing beginbfrange")
				panic("missing beginbfrange")
			}
			for i := 0; i < n; i++ {
				dst, srcHi, srcLo := stk.Pop(), stk.Pop().RawString(), stk.Pop().RawString()
				m.bfrange = append(m.bfrange, bfrange{srcLo, srcHi, dst})
			}
		case "defineresource":
			stk.Pop().Name() // category
			value := stk.Pop()
			stk.Pop().Name() // key
			stk.Push(value)
		default:
			if DebugOn {
				println("interp\t", op)
			}
		}
	})
	if !ok {
		return nil
	}
	return &m
}

type matrix [3][3]float64

var ident = matrix{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}

func (x matrix) mul(y matrix) matrix {
	var z matrix
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			for k := 0; k < 3; k++ {
				z[i][j] += x[i][k] * y[k][j]
			}
		}
	}
	return z
}

// A Text represents a single piece of text drawn on a page.
type Text struct {
	Font     string  // the font used
	FontSize float64 // the font size, in points (1/72 of an inch)
	X        float64 // the X coordinate, in points, increasing left to right
	Y        float64 // the Y coordinate, in points, increasing bottom to top
	W        float64 // the width of the text, in points
	S        string  // the actual UTF-8 text
}

// A Rect represents a rectangle.
type Rect struct {
	Min, Max Point
}

// A Point represents an X, Y pair.
type Point struct {
	X float64
	Y float64
}

// Content describes the basic content on a page: the text and any drawn rectangles.
type Content struct {
	Text []Text
	Rect []Rect
}

type gstate struct {
	Tc    float64
	Tw    float64
	Th    float64
	Tl    float64
	Tf    Font
	Tfs   float64
	Tmode int
	Trise float64
	Tm    matrix
	Tlm   matrix
	Trm   matrix
	CTM   matrix
}

// GetPlainText returns the page's all text without format.
// fonts can be passed in (to improve parsing performance) or left nil
func (p Page) GetPlainText(fonts map[string]*Font) (result string, err error) {
	defer func() {
		if r := recover(); r != nil {
			result = ""
			logger.Error(fmt.Sprint(r))
			err = errors.New(fmt.Sprint(r))
		}
	}()

	// Handle in case the content page is empty
	if p.V.IsNull() || p.V.Key("Contents").Kind() == Null {
		return "", nil
	}
	strm := p.V.Key("Contents")
	var enc TextEncoding = &nopEncoder{}

	logger.Debug(fmt.Sprintf("contents: obj %d %d stream (declLen=%d)",
		strm.ptr.id, strm.ptr.gen, strm.Key("Length").Int64()), true)
	if fonts == nil {
		fonts = make(map[string]*Font)
		for _, font := range p.Fonts() {
			f := p.Font(font)
			fonts[font] = &f
		}
	}

	var textBuilder bytes.Buffer
	showText := func(s string) {
		textBuilder.WriteString(s)
	}
	showEncodedText := func(s string) {
		for _, ch := range enc.Decode(s) {
			_, err := textBuilder.WriteRune(ch)
			if err != nil {
				logger.Error(err.Error())
				panic(err)
			}
		}
	}
	logger.Debug("Parsing content", true)

	Interpret(strm, func(stk *Stack, op string) {
		n := stk.Len()
		args := make([]Value, n)
		for i := n - 1; i >= 0; i-- {
			args[i] = stk.Pop()
		}

		switch op {
		default:
			// Easier debug
			// fmt.Println("<DEBUG><op>", op, "</op><args>", args, "</args>")
			return
		case "BT": // add a space between text objects
			logger.Debug("operator: BT", true)
			showText("\n")
		case "T*": // move to start of next line
			showEncodedText("\n")
		case "Tf": // set text font and size
			logger.Debug(fmt.Sprintf("operator: Tf (%s %v)", args[0].Name(), args[1].Float64()), true)
			if len(args) != 2 {
				logger.Error("bad TL")
				panic("bad TL")
			}
			if font, ok := fonts[args[0].Name()]; ok {
				enc = font.Encoder()
			} else {
				enc = &nopEncoder{}
			}

		case "\"": // set spacing, move to next line, and show text
			if len(args) != 3 {
				logger.Error("bad \" operator")
				panic("bad \" operator")
			}
			fallthrough
		case "'": // move to next line and show text
			if len(args) != 1 {
				logger.Error("bad ' operator")
				panic("bad ' operator")
			}
			fallthrough
		case "Tj": // show text
			if len(args) != 1 {
				logger.Error("bad Tj operator")
				panic("bad Tj operator")
			}
			raw := args[0].RawString()
			mapped := enc.Decode(raw)
			logger.Debug(fmt.Sprintf("operator: Tj -> bytes=%#x -> mapped %q", []byte(raw), mapped), true)
			showEncodedText(raw)
		case "TJ": // show text, allowing individual glyph positioning
			v := args[0]
			for i := 0; i < v.Len(); i++ {
				x := v.Index(i)
				if x.Kind() == String {
					showEncodedText(x.RawString())
				}
			}
			logger.Debug("operator: TJ", true)
		}
	})

	logger.Debug("Completed content parsing", true)

	return textBuilder.String(), nil
}

// Column represents the contents of a column
type Column struct {
	Position int64
	Content  TextVertical
}

// Columns is a list of column
type Columns []*Column

// GetTextByColumn returns the page's all text grouped by column
func (p Page) GetTextByColumn() (Columns, error) {
	logger.Debug("retreiving all text grouped by column")

	result := Columns{}
	var err error

	defer func() {
		if r := recover(); r != nil {
			result = Columns{}
			err = errors.New(fmt.Sprint(r))
		}
	}()

	showText := func(enc TextEncoding, currentX, currentY float64, s string) {
		var textBuilder bytes.Buffer

		for _, ch := range enc.Decode(s) {
			_, err := textBuilder.WriteRune(ch)
			if err != nil {
				panic(err)
			}
		}
		text := Text{
			S: textBuilder.String(),
			X: currentX,
			Y: currentY,
		}

		var currentColumn *Column
		columnFound := false
		for _, column := range result {
			if int64(currentX) == column.Position {
				currentColumn = column
				columnFound = true
				break
			}
		}

		if !columnFound {
			currentColumn = &Column{
				Position: int64(currentX),
				Content:  TextVertical{},
			}
			result = append(result, currentColumn)
		}

		currentColumn.Content = append(currentColumn.Content, text)
	}

	p.walkTextBlocks(showText)

	for _, column := range result {
		sort.Sort(column.Content)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Position < result[j].Position
	})

	return result, err
}

// Row represents the contents of a row
type Row struct {
	Position int64
	Content  TextHorizontal
}

// Rows is a list of rows
type Rows []*Row

// GetTextByRow returns the page's all text grouped by rows
func (p Page) GetTextByRow() (Rows, error) {
	logger.Debug("retrieving all text grouped by columns")

	result := Rows{}
	var err error

	defer func() {
		if r := recover(); r != nil {
			result = Rows{}
			err = errors.New(fmt.Sprint(r))
		}
	}()

	showText := func(enc TextEncoding, currentX, currentY float64, s string) {
		var textBuilder bytes.Buffer
		for _, ch := range enc.Decode(s) {
			_, err := textBuilder.WriteRune(ch)
			if err != nil {
				panic(err)
			}
		}

		// if DebugOn {
		// 	fmt.Println(textBuilder.String())
		// }

		text := Text{
			S: textBuilder.String(),
			X: currentX,
			Y: currentY,
		}

		var currentRow *Row
		rowFound := false
		for _, row := range result {
			if int64(currentY) == row.Position {
				currentRow = row
				rowFound = true
				break
			}
		}

		if !rowFound {
			currentRow = &Row{
				Position: int64(currentY),
				Content:  TextHorizontal{},
			}
			result = append(result, currentRow)
		}

		currentRow.Content = append(currentRow.Content, text)
	}

	p.walkTextBlocks(showText)

	for _, row := range result {
		sort.Sort(row.Content)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Position > result[j].Position
	})

	return result, err
}

func (p Page) walkTextBlocks(walker func(enc TextEncoding, x, y float64, s string)) {
	logger.Debug(fmt.Sprintf("walkTextBlocks: processing text content for Page %d %d R", p.V.ptr.id, p.V.ptr.gen))

	// Handle in case the content page is empty
	if p.V.IsNull() || p.V.Key("Contents").Kind() == Null {
		return
	}

	strm := p.V.Key("Contents")

	fonts := make(map[string]*Font)
	for _, font := range p.Fonts() {
		f := p.Font(font)
		fonts[font] = &f
	}

	var enc TextEncoding = &nopEncoder{}
	var currentX, currentY float64
	Interpret(strm, func(stk *Stack, op string) {
		n := stk.Len()
		args := make([]Value, n)
		for i := n - 1; i >= 0; i-- {
			args[i] = stk.Pop()
		}
		switch op {
		default:
			return
		case "T*": // move to start of next line
		case "Tf": // set text font and size
			if len(args) != 2 {
				panic("bad TL")
			}

			if font, ok := fonts[args[0].Name()]; ok {
				enc = font.Encoder()
			} else {
				enc = &nopEncoder{}
			}
		case "\"": // set spacing, move to next line, and show text
			if len(args) != 3 {
				panic("bad \" operator")
			}
			fallthrough
		case "'": // move to next line and show text
			if len(args) != 1 {
				panic("bad ' operator")
			}
			fallthrough
		case "Tj": // show text
			if len(args) != 1 {
				panic("bad Tj operator")
			}

			walker(enc, currentX, currentY, args[0].RawString())
		case "TJ": // show text, allowing individual glyph positioning
			v := args[0]
			for i := 0; i < v.Len(); i++ {
				x := v.Index(i)
				if x.Kind() == String {
					walker(enc, currentX, currentY, x.RawString())
				}
			}
		case "Td":
			walker(enc, currentX, currentY, "")
		case "Tm":
			currentX = args[4].Float64()
			currentY = args[5].Float64()
		}
	})
}

// Content returns the page's content.
func (p Page) Content() Content {
	logger.Debug(fmt.Sprintf("Content: starting content extraction for Page %d %d R", p.V.ptr.id, p.V.ptr.gen))
	// Handle in case the content page is empty
	if p.V.IsNull() || p.V.Key("Contents").Kind() == Null {
		return Content{}
	}
	strm := p.V.Key("Contents")
	var enc TextEncoding = &nopEncoder{}

	var g = gstate{
		Th:  1,
		CTM: ident,
	}

	var text []Text
	showText := func(s string) {
		n := 0
		decoded := enc.Decode(s)
		for _, ch := range decoded {
			var w0 float64
			if n < len(s) {
				w0 = g.Tf.Width(int(s[n]))
			}
			n++

			f := g.Tf.BaseFont()
			if i := strings.Index(f, "+"); i >= 0 {
				f = f[i+1:]
			}

			Trm := matrix{{g.Tfs * g.Th, 0, 0}, {0, g.Tfs, 0}, {0, g.Trise, 1}}.mul(g.Tm).mul(g.CTM)
			text = append(text, Text{f, Trm[0][0], Trm[2][0], Trm[2][1], w0 / 1000 * Trm[0][0], string(ch)})

			tx := w0/1000*g.Tfs + g.Tc
			tx *= g.Th
			g.Tm = matrix{{1, 0, 0}, {0, 1, 0}, {tx, 0, 1}}.mul(g.Tm)
		}
	}

	var rect []Rect
	var gstack []gstate
	Interpret(strm, func(stk *Stack, op string) {
		n := stk.Len()
		args := make([]Value, n)
		for i := n - 1; i >= 0; i-- {
			args[i] = stk.Pop()
		}
		switch op {
		default:
			// if DebugOn {
			// 	fmt.Println(op, args)
			// }
			return

		case "cm": // update g.CTM
			if len(args) != 6 {
				panic("bad g.Tm")
			}
			var m matrix
			for i := 0; i < 6; i++ {
				m[i/2][i%2] = args[i].Float64()
			}
			m[2][2] = 1
			g.CTM = m.mul(g.CTM)

		case "gs": // set parameters from graphics state resource
			//gs := p.Resources().Key("ExtGState").Key(args[0].Name())
			//font := gs.Key("Font")
			//if font.Kind() == Array && font.Len() == 2 {
			// if DebugOn {
			// 	fmt.Println("FONT", font)
			// }
			//}

		case "f": // fill
		case "g": // setgray
		case "l": // lineto
		case "m": // moveto

		case "cs": // set colorspace non-stroking
		case "scn": // set color non-stroking

		case "re": // append rectangle to path
			if len(args) != 4 {
				panic("bad re")
			}
			x, y, w, h := args[0].Float64(), args[1].Float64(), args[2].Float64(), args[3].Float64()
			rect = append(rect, Rect{Point{x, y}, Point{x + w, y + h}})

		case "q": // save graphics state
			gstack = append(gstack, g)

		case "Q": // restore graphics state
			n := len(gstack) - 1
			g = gstack[n]
			gstack = gstack[:n]

		case "BT": // begin text (reset text matrix and line matrix)
			g.Tm = ident
			g.Tlm = g.Tm

		case "ET": // end text

		case "T*": // move to start of next line
			x := matrix{{1, 0, 0}, {0, 1, 0}, {0, -g.Tl, 1}}
			g.Tlm = x.mul(g.Tlm)
			g.Tm = g.Tlm

		case "Tc": // set character spacing
			if len(args) != 1 {
				logger.Error("bad g.Tc")
				panic("bad g.Tc")
			}
			g.Tc = args[0].Float64()

		case "TD": // move text position and set leading
			if len(args) != 2 {
				logger.Error("bad Td")
				panic("bad Td")
			}
			g.Tl = -args[1].Float64()
			fallthrough
		case "Td": // move text position
			if len(args) != 2 {
				logger.Error("bad Td")
				panic("bad Td")
			}
			tx := args[0].Float64()
			ty := args[1].Float64()
			x := matrix{{1, 0, 0}, {0, 1, 0}, {tx, ty, 1}}
			g.Tlm = x.mul(g.Tlm)
			g.Tm = g.Tlm

		case "Tf": // set text font and size
			if len(args) != 2 {
				logger.Error("bad TL")
				panic("bad TL")
			}
			f := args[0].Name()
			g.Tf = p.Font(f)
			enc = g.Tf.Encoder()
			if enc == nil {
				if DebugOn {
					println("no cmap for", f)
				}
				logger.Debug(fmt.Sprintf("no cmap for %s", f))
				enc = &nopEncoder{}
			}
			g.Tfs = args[1].Float64()

		case "\"": // set spacing, move to next line, and show text
			if len(args) != 3 {
				logger.Error("bad \" operator")
				panic("bad \" operator")
			}
			g.Tw = args[0].Float64()
			g.Tc = args[1].Float64()
			args = args[2:]
			fallthrough
		case "'": // move to next line and show text
			if len(args) != 1 {
				logger.Error("bad ' operator")
				panic("bad ' operator")
			}
			x := matrix{{1, 0, 0}, {0, 1, 0}, {0, -g.Tl, 1}}
			g.Tlm = x.mul(g.Tlm)
			g.Tm = g.Tlm
			fallthrough
		case "Tj": // show text
			if len(args) != 1 {
				logger.Error("bad Tj operator")
				panic("bad Tj operator")
			}
			showText(args[0].RawString())

		case "TJ": // show text, allowing individual glyph positioning
			v := args[0]
			for i := 0; i < v.Len(); i++ {
				x := v.Index(i)
				if x.Kind() == String {
					showText(x.RawString())
				} else {
					tx := -x.Float64() / 1000 * g.Tfs * g.Th
					g.Tm = matrix{{1, 0, 0}, {0, 1, 0}, {tx, 0, 1}}.mul(g.Tm)
				}
			}
			showText("\n")

		case "TL": // set text leading
			if len(args) != 1 {
				logger.Error("bad TL")
				panic("bad TL")
			}
			g.Tl = args[0].Float64()

		case "Tm": // set text matrix and line matrix
			if len(args) != 6 {
				logger.Error("bad g.Tm")
				panic("bad g.Tm")
			}
			var m matrix
			for i := 0; i < 6; i++ {
				m[i/2][i%2] = args[i].Float64()
			}
			m[2][2] = 1
			g.Tm = m
			g.Tlm = m

		case "Tr": // set text rendering mode
			if len(args) != 1 {
				logger.Error("bad Tr")
				panic("bad Tr")
			}
			g.Tmode = int(args[0].Int64())

		case "Ts": // set text rise
			if len(args) != 1 {
				logger.Error("bad Ts")
				panic("bad Ts")
			}
			g.Trise = args[0].Float64()

		case "Tw": // set word spacing
			if len(args) != 1 {
				logger.Error("bad g.Tw")
				panic("bad g.Tw")
			}
			g.Tw = args[0].Float64()

		case "Tz": // set horizontal text scaling
			if len(args) != 1 {
				logger.Error("bad Tz")
				panic("bad Tz")
			}
			g.Th = args[0].Float64() / 100
		}
	})
	return Content{text, rect}
}

// TextVertical implements sort.Interface for sorting
// a slice of Text values in vertical order, top to bottom,
// and then left to right within a line.
type TextVertical []Text

func (x TextVertical) Len() int      { return len(x) }
func (x TextVertical) Swap(i, j int) { x[i], x[j] = x[j], x[i] }
func (x TextVertical) Less(i, j int) bool {
	if x[i].Y != x[j].Y {
		return x[i].Y > x[j].Y
	}
	return x[i].X < x[j].X
}

// TextHorizontal implements sort.Interface for sorting
// a slice of Text values in horizontal order, left to right,
// and then top to bottom within a column.
type TextHorizontal []Text

func (x TextHorizontal) Len() int      { return len(x) }
func (x TextHorizontal) Swap(i, j int) { x[i], x[j] = x[j], x[i] }
func (x TextHorizontal) Less(i, j int) bool {
	if x[i].X != x[j].X {
		return x[i].X < x[j].X
	}
	return x[i].Y > x[j].Y
}

// An Outline is a tree describing the outline (also known as the table of contents)
// of a document.
type Outline struct {
	Title string    // title for this element
	Child []Outline // child elements
}

// Outline returns the document outline.
// The Outline returned is the root of the outline tree and typically has no Title itself.
// That is, the children of the returned root are the top-level entries in the outline.
func (r *Reader) Outline() Outline {

	return buildOutline(r.Trailer().Key("Root").Key("Outlines"))
}

func buildOutline(entry Value) Outline {
	var x Outline
	x.Title = entry.Key("Title").Text()
	for child := entry.Key("First"); child.Kind() == Dict; child = child.Key("Next") {
		x.Child = append(x.Child, buildOutline(child))
	}
	return x
}
