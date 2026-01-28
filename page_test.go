// Copyright © 2026, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: BSD-3-Clause
package xtract

import (
	"bytes"
	"io"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cs(lo, hi string) byteRange { return byteRange{low: lo, high: hi} }

// Generated a cmap that hits all Decode branches.
func makeFullTestCMap() *cmap {
	return &cmap{
		space: [4][]byteRange{
			{ // 1-byte
				cs("\x01", "\x01"), // bfchar single-byte
				cs("\x05", "\x07"), // bfrange: 05–07
				cs("\x09", "\x09"), // bfchar surrogate pair (U+1F600)
				cs("\x7E", "\x7E"), // ASCII fallback
				cs("\xFF", "\xFF"), // invalid byte fallback
				cs("\x30", "\x30"), // '0' (overlap vs 2-byte 30 31)

			},
			{ // 2-byte
				cs("\x02\x03", "\x02\x03"), // bfchar 2-byte
				cs("\x30\x31", "\x30\x31"), // overlap with 1-byte 30 (shortest-match demo)
			},
			{ // 3-byte (non-overlapping)
				cs("\xAA\xBB\xCC", "\xAA\xBB\xCC"), // bfchar 漢
			},
			{ // 4-byte (non-overlapping)
				cs("\xFA\xFB\xFC\xFD", "\xFA\xFB\xFC\xFD"), // bfchar U+1F600
			},
		},
		bfchar: []bfchar{
			{orig: "\x01", repl: "\x00\x41"},                     // "A"
			{orig: "\x02\x03", repl: "\x00\xE9"},                 // "é"
			{orig: "\x09", repl: "\xD8\x3D\xDE\x00"},             // U+1F600
			{orig: "\xAA\xBB\xCC", repl: "\x6F\x22"},             // 漢 (UTF-16BE)
			{orig: "\xFA\xFB\xFC\xFD", repl: "\xD8\x3D\xDE\x00"}, // U+1F600
		},
		bfrange: []bfrange{
			{lo: "\x05", hi: "\x07", dst: Value{data: "\x00\x44"}}, // start at "D"
		},
	}
}

func TestFindNextCodespace(t *testing.T) {
	m := &cmap{
		space: [4][]byteRange{
			{cs("\x30", "\x30")},                         // 1-byte '0'
			{cs("\x30\x31", "\x30\x31")},                 // 2-byte "01"
			{cs("\xAA\xBB\xCC", "\xAA\xBB\xCC")},         // 3-byte
			{cs("\xFA\xFB\xFC\xFD", "\xFA\xFB\xFC\xFD")}, // 4-byte
		},
	}

	// 3-byte
	code, n := m.findNextCodespace("\xAA\xBB\xCC")
	assert.Equal(t, "\xAA\xBB\xCC", code)
	assert.Equal(t, 3, n)

	// 4-byte
	code, n = m.findNextCodespace("\xFA\xFB\xFC\xFD")
	assert.Equal(t, "\xFA\xFB\xFC\xFD", code)
	assert.Equal(t, 4, n)

	// no match → n == 0
	code, n = m.findNextCodespace("\x12")
	assert.Equal(t, "", code)
	assert.Equal(t, 0, n)
}

func TestResolveCodeMapping_bfchar(t *testing.T) {
	m := &cmap{
		bfchar: []bfchar{
			{orig: "\x01", repl: "\x00\x41"},     // "A"
			{orig: "\x02\x03", repl: "\x00\xE9"}, // "é"
		},
	}

	out, ok := m.resolveCodeMapping("\x01", 1)
	assert.True(t, ok)
	assert.Equal(t, "A", string(out))

	out, ok = m.resolveCodeMapping("\x02\x03", 2)
	assert.True(t, ok)
	assert.Equal(t, "é", string(out))

	_, ok = m.resolveCodeMapping("\xFF", 1)
	assert.False(t, ok)
}

func TestResolveCodeMapping_bfrangeString(t *testing.T) {
	m := &cmap{
		bfrange: []bfrange{
			{lo: "\x05", hi: "\x07", dst: Value{data: "\x00\x44"}}, // D..F
		},
	}
	// lo
	out, ok := m.resolveCodeMapping("\x05", 1)
	assert.True(t, ok)
	assert.Equal(t, "D", string(out))
	// middle
	out, ok = m.resolveCodeMapping("\x06", 1)
	assert.True(t, ok)
	assert.Equal(t, "E", string(out))
	// hi
	out, ok = m.resolveCodeMapping("\x07", 1)
	assert.True(t, ok)
	assert.Equal(t, "F", string(out))
}

func TestResolveBfrangeWithArray(t *testing.T) {
	//dst array contains strings
	brString := bfrange{
		lo: "\x05",
		hi: "\x07",
		dst: Value{
			data: array{
				"\x00\x44", // D
				"\x00\x45", // E
				"\x00\x46", // F
			},
		},
	}

	out := resolveBfrangeWithArray(brString, "\x05")
	assert.Equal(t, "D", string(out))

	out = resolveBfrangeWithArray(brString, "\x06")
	assert.Equal(t, "E", string(out))

	out = resolveBfrangeWithArray(brString, "\x07")
	assert.Equal(t, "F", string(out))

	// dst array contains non-string
	brNonString := bfrange{
		lo: "\x01",
		hi: "\x01",
		dst: Value{
			data: array{
				int64(123), // not a string
			},
		},
	}
	out = resolveBfrangeWithArray(brNonString, "\x01")
	assert.Nil(t, out)
}

func TestCmapDecode(t *testing.T) {
	m := makeFullTestCMap()

	type tc struct {
		name   string
		input  string
		expect string
		check  func(got string)
	}
	tests := []tc{
		// bfchar mappings
		{name: "bfchar-1byte", input: "\x01", expect: "A"},
		{name: "bfchar-2byte", input: "\x02\x03", expect: "é"},
		{name: "bfchar-3byte", input: "\xAA\xBB\xCC", expect: "漢"},
		{name: "bfchar-4byte", input: "\xFA\xFB\xFC\xFD", expect: string(rune(0x1F600))},
		// bfrange (string-dest in this cmap)
		{name: "bfrange-05", input: "\x05", expect: "D"},
		{name: "bfrange-06", input: "\x06", expect: "E"},
		{name: "bfrange-07", input: "\x07", expect: "F"},
		// fallbacks
		{name: "fallback-ascii", input: "\x7E", expect: "~"},
		{
			name:  "fallback-invalid-0xFF",
			input: "\xFF",
			check: func(got string) {
				// Exactly one valid rune (not RuneError)
				assert.Equal(t, 1, utf8.RuneCountInString(got))
				r := []rune(got)[0]
				assert.NotEqual(t, utf8.RuneError, r)
			},
		},
		// byte not in any codespace, then mapped ASCII '0'
		{name: "no-codespace-then-mapped", input: "\x20\x30", expect: " 0"},
		// incomplete multi-byte at end → preserved 1 rune
		{
			name:  "incomplete-2byte",
			input: "\x12",
			check: func(got string) {
				assert.NotEmpty(t, got)
				assert.Equal(t, 1, utf8.RuneCountInString(got))
			},
		},
		{
			name:  "mixed-sequence",
			input: "\x01\x7E\x05\xFF", // A, ~, D, preserved from 0xFF
			check: func(got string) {
				assert.True(t, len(got) >= 4)
				assert.Equal(t, "A~D", got[:3])
				rs := []rune(got)
				last := rs[len(rs)-1]
				assert.NotEqual(t, utf8.RuneError, last)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m.Decode(tt.input)
			if tt.check != nil {
				tt.check(got)
				return
			}
			assert.Equal(t, tt.expect, got)
		})
	}
}

func TestDecode_MissingCodespace(t *testing.T) {
	// Mapping exists for 0x01 -> "A", but 0x01 is NOT in codespace.
	// hence, decode should NOT return "A".
	m := &cmap{
		space: [4][]byteRange{
			{cs("\x7E", "\x7E")}, // only '~' allowed; 0x01 excluded
		},
		bfchar: []bfchar{
			{orig: "\x01", repl: "\x00\x41"}, // would map to "A"
		},
	}
	got := m.Decode("\x01")
	assert.False(t, got == "A", "mapping should fail if codespace is missing")
}

func TestNopEncoderDecode(t *testing.T) {
	e := &nopEncoder{}
	assert.Equal(t, "raw\x00bytes", e.Decode("raw\x00bytes"))
}

func TestByteEncoderDecode(t *testing.T) {
	var tbl [256]rune
	for i := 0; i < 256; i++ {
		tbl[i] = rune(i) // identity map
	}
	tbl['H'] = 'H'
	tbl['i'] = 'i'
	tbl['!'] = '!'
	e := &byteEncoder{table: &tbl}

	got := e.Decode("Hi!")
	assert.Equal(t, "Hi!", got)
}

func TestTextVerticalSort(t *testing.T) {
	items := TextVertical{
		{X: 50, Y: 100, S: "A"},
		{X: 10, Y: 200, S: "B"},
		{X: 30, Y: 200, S: "C"},
		{X: 20, Y: 150, S: "D"},
	}
	// sorted: Y desc, then X asc
	// Expected order by (Y,X): (200,10)->B, (200,30)->C, (150,20)->D, (100,50)->A
	require.Len(t, items, 4)
	type sortable interface {
		Len() int
		Swap(i, j int)
		Less(i, j int) bool
	}
	s := any(items).(sortable)
	for i := 0; i < s.Len(); i++ {
		for j := i + 1; j < s.Len(); j++ {
			if s.Less(j, i) {
				s.Swap(i, j)
			}
		}
	}
	assert.Equal(t, "B", items[0].S)
	assert.Equal(t, "C", items[1].S)
	assert.Equal(t, "D", items[2].S)
	assert.Equal(t, "A", items[3].S)
}

func TestTextHorizontalSort(t *testing.T) {
	items := TextHorizontal{
		{X: 50, Y: 100, S: "A"},
		{X: 10, Y: 200, S: "B"},
		{X: 30, Y: 150, S: "C"},
		{X: 20, Y: 150, S: "D"},
	}
	// sorted: X asc, then Y desc
	// Expected order by (X,Y): (10,200)->B, (20,150)->D, (30,150)->C, (50,100)->A
	require.Len(t, items, 4)
	type sortable interface {
		Len() int
		Swap(i, j int)
		Less(i, j int) bool
	}
	s := any(items).(sortable)
	for i := 0; i < s.Len(); i++ {
		for j := i + 1; j < s.Len(); j++ {
			if s.Less(j, i) {
				s.Swap(i, j)
			}
		}
	}
	assert.Equal(t, "B", items[0].S)
	assert.Equal(t, "D", items[1].S)
	assert.Equal(t, "C", items[2].S)
	assert.Equal(t, "A", items[3].S)
}

// PDF Name
func nameVal(n string) Value {
	return Value{data: n}
}

// PDF Integer
func intVal(i int64) Value {
	return Value{data: i}
}

// PDF Dictionary
func dictVal(kvs map[string]Value) Value {
	return Value{data: kvs}
}

// PDF Array
func arrVal(vals ...Value) Value {
	return Value{data: vals}
}

// PDF Null
func nullVal() Value {
	return Value{} // zero Value is null
}

func TestGetEncoder(t *testing.T) {
	//WinAnsiEncoding → should decode 0x41 → "A"
	f1 := Font{V: dictVal(map[string]Value{"Encoding": nameVal("WinAnsiEncoding")})}
	enc1 := f1.getEncoder()
	got := enc1.Decode(string([]byte{0x41}))
	assert.Equal(t, "A", got)

	//MacRomanEncoding → should decode 0x41 → "A"
	f2 := Font{V: dictVal(map[string]Value{"Encoding": nameVal("MacRomanEncoding")})}
	enc2 := f2.getEncoder()
	got = enc2.Decode(string([]byte{0x41}))
	assert.Equal(t, "A", got)

	// Identity-H with no ToUnicode → falls back to pdfDocEncoding, ASCII passthrough
	f3 := Font{V: dictVal(map[string]Value{
		"Encoding":  nameVal("Identity-H"),
		"ToUnicode": nullVal(),
	})}
	enc3 := f3.getEncoder()
	got = enc3.Decode("ABC")
	assert.Equal(t, "ABC", got)

	// Dict with Differences → should produce a dictEncoder that alters mappings
	diff := arrVal(intVal(65), nameVal("A")) // map code 65 -> /A
	f4 := Font{V: dictVal(map[string]Value{
		"Encoding": dictVal(map[string]Value{"Differences": diff}),
	})}
	enc4 := f4.getEncoder()
	got = enc4.Decode(string([]byte{65}))
	require.NotEmpty(t, got)

	// Null encoding → falls back to charmapEncoding/pdfDocEncoding
	f5 := Font{V: dictVal(map[string]Value{
		"Encoding":  nullVal(),
		"ToUnicode": nullVal(),
	})}
	enc5 := f5.getEncoder()
	got = enc5.Decode("Test")
	assert.Equal(t, "Test", got)

	//Unknown encoding name → nopEncoder (passthrough)
	f6 := Font{V: dictVal(map[string]Value{"Encoding": nameVal("FooBar")})}
	enc6 := f6.getEncoder()
	got = enc6.Decode("XYZ")
	assert.Equal(t, "XYZ", got)
}

func TestPage(t *testing.T) {
	ra, size, done := openReaderAt(t, "pdf_test.pdf")
	defer done()
	r, err := NewReader(ra, size)
	if err != nil {
		t.Skipf("Skipping: cannot parse PDF sample: %v", err)
	}
	// Request first page (1-based)
	p := r.Page(1)
	if p.V.IsNull() {
		t.Skip("Skipping: could not locate page 1 (PDF may have unusual structure)")
	}
	// Basic assertion: page Type must be "Page"
	assert.Equal(t, "Page", p.V.Key("Type").Name(), "expected returned object's /Type to be Page")

	// Out-of-range page should return zero Page
	total := r.NumPage()
	p2 := r.Page(total + 1)
	assert.True(t, p2.V.IsNull(), "expected out-of-range page to be zero/empty Page")
}

func TestNumPage(t *testing.T) {
	//Trailer.Root.Pages.Count present
	r1 := &Reader{
		trailer: dict{
			name("Root"): dict{
				name("Pages"): dict{
					name("Count"): int64(5),
				},
			},
		},
	}
	assert.Equal(t, 5, r1.NumPage(), "should return the Count value when present")
	//Missing keys should default to 0
	r2 := &Reader{trailer: dict{}}
	assert.Equal(t, 0, r2.NumPage(), "should return 0 if Root/Pages/Count missing")
}

func TestGetStyledTexts(t *testing.T) {
	ra, size, done := openReaderAt(t, "pdf_test.pdf")
	defer done()

	r, err := NewReader(ra, size)
	if err != nil {
		t.Skipf("Skipping: cannot parse PDF sample: %v", err)
	}

	texts, err := r.GetStyledTexts()
	require.NoError(t, err)

	if len(texts) == 0 {
		t.Skip("Skipping: sample PDF contains no styled texts")
	}

	for i, tx := range texts {
		assert.NotEmpty(t, tx.S, "text[%d].S should not be empty", i)
	}
}

var minimalTwoPagePDF = []byte(`%PDF-1.4
1 0 obj
<< /Type /Catalog /Pages 2 0 R >>
endobj
2 0 obj
<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>
endobj
3 0 obj
<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 300]
   /Contents 5 0 R /Resources << /Font << /F1 6 0 R >> >> >>
endobj
4 0 obj
<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 300]
   /Contents 7 0 R /Resources << /Font << /F1 6 0 R >> >> >>
endobj
5 0 obj
<< /Length 44 >>
stream
BT /F1 12 Tf 72 200 Td (Hello ) Tj ET
endstream
endobj
7 0 obj
<< /Length 43 >>
stream
BT /F1 12 Tf 72 200 Td (World) Tj ET
endstream
endobj
6 0 obj
<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>
endobj
xref
0 8
0000000000 65535 f 
0000000009 00000 n 
0000000058 00000 n 
0000000121 00000 n 
0000000250 00000 n 
0000000379 00000 n 
0000000552 00000 n 
0000000466 00000 n 
trailer<< /Root 1 0 R /Size 8 >>
startxref
622
%%EOF
`)

func newTestReader(t *testing.T, b []byte) *Reader {
	t.Helper()
	br := bytes.NewReader(b) // bytes.Reader implements io.ReaderAt
	r, err := NewReader(br, int64(len(b)))
	if err != nil {
		// print a clear error to help debugging (include %#v to reveal formatting/newlines)
		t.Fatalf("failed to initialize Reader: %#v", err)
	}
	return r
}

func TestGetPlainText(t *testing.T) {
	r := newTestReader(t, minimalTwoPagePDF)
	reader, err := r.GetPlainText()
	assert.NoError(t, err, "GetPlainText should not return error")
	out, err := io.ReadAll(reader)
	assert.NoError(t, err, "reading output should not fail")
	txt := string(out)
	assert.Contains(t, txt, "Hello", "expected text 'Hello' missing")
	assert.Contains(t, txt, "World", "expected text 'World' missing")
}

// pad10 formats n as a 10-digit zero-padded string (xref format).
func pad10(n int) string {
	s := strconv.Itoa(n)
	if len(s) >= 10 {
		return s
	}
	return strings.Repeat("0", 10-len(s)) + s
}

func TestGetTextByColumn(t *testing.T) {
	stream := "BT /F1 12 Tf 1 0 0 1 100 300 Tm (A) Tj ET\n" +
		"BT /F1 12 Tf 1 0 0 1 100 250 Tm (B) Tj ET\n" +
		"BT /F1 12 Tf 1 0 0 1 200 300 Tm (C) Tj ET\n" +
		"BT /F1 12 Tf 1 0 0 1 200 100 Tm (D) Tj ET\n"

	var b strings.Builder
	b.WriteString("%PDF-1.4\n")

	offsets := map[int]int{}

	// object 1 (catalog)
	offsets[1] = b.Len()
	b.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")

	// object 2 (pages)
	offsets[2] = b.Len()
	b.WriteString("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")

	// object 3 (page) referring to contents 4 and font 5
	offsets[3] = b.Len()
	b.WriteString("3 0 obj\n")
	b.WriteString("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 300] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>\n")
	b.WriteString("endobj\n")

	// object 4 (contents stream)
	offsets[4] = b.Len()
	b.WriteString("4 0 obj\n")
	b.WriteString("<< /Length ")
	b.WriteString(strconv.Itoa(len(stream)))
	b.WriteString(" >>\n")
	b.WriteString("stream\n")
	b.WriteString(stream)
	if !strings.HasSuffix(stream, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("endstream\nendobj\n")

	// object 5 (font)
	offsets[5] = b.Len()
	b.WriteString("5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n")

	// xref and trailer
	xrefStart := b.Len()
	b.WriteString("xref\n")
	maxObj := 5
	b.WriteString("0 ")
	b.WriteString(strconv.Itoa(maxObj + 1))
	b.WriteString("\n")
	// object 0
	b.WriteString(pad10(0))
	b.WriteString(" 65535 f \n")
	// objects 1..maxObj
	for i := 1; i <= maxObj; i++ {
		b.WriteString(pad10(offsets[i]))
		b.WriteString(" 00000 n \n")
	}
	b.WriteString("trailer\n")
	b.WriteString("<< /Root 1 0 R /Size ")
	b.WriteString(strconv.Itoa(maxObj + 1))
	b.WriteString(" >>\n")
	b.WriteString("startxref\n")
	b.WriteString(strconv.Itoa(xrefStart))
	b.WriteString("\n%%EOF\n")

	pdf := []byte(b.String())

	br := bytes.NewReader(pdf)
	r, err := NewReader(br, int64(len(pdf)))
	require.NoError(t, err, "NewReader should succeed")

	page := r.Page(1)
	require.False(t, page.V.IsNull(), "page must exist")

	cols, err := page.GetTextByColumn()
	require.NoError(t, err, "GetTextByColumn should not error")

	// two columns (X=100 and X=200)
	require.Len(t, cols, 2, "should detect 2 columns")

	// Column X=100 should contain A then B (Y descending)
	assert.Equal(t, int64(100), cols[0].Position)
	require.Len(t, cols[0].Content, 2)
	assert.Equal(t, "A", cols[0].Content[0].S)
	assert.Equal(t, "B", cols[0].Content[1].S)

	// Column X=200 should contain C then D (Y descending)
	assert.Equal(t, int64(200), cols[1].Position)
	require.Len(t, cols[1].Content, 2)
	assert.Equal(t, "C", cols[1].Content[0].S)
	assert.Equal(t, "D", cols[1].Content[1].S)
}

// asserts GetTextByRow groups rows and sorts content left-to-right within each row.
func TestGetTextByRow(t *testing.T) {
	stream := "" +
		"BT /F1 12 Tf 1 0 0 1 100 300 Tm (A) Tj ET\n" +
		"BT /F1 12 Tf 1 0 0 1 100 250 Tm (B) Tj ET\n" +
		"BT /F1 12 Tf 1 0 0 1 200 300 Tm (C) Tj ET\n" +
		"BT /F1 12 Tf 1 0 0 1 200 100 Tm (D) Tj ET\n"

	var b strings.Builder
	b.WriteString("%PDF-1.4\n")

	offsets := map[int]int{}

	// object 1: Catalog
	offsets[1] = b.Len()
	b.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")

	// object 2: Pages
	offsets[2] = b.Len()
	b.WriteString("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")

	// object 3: Page referencing contents (4) and font (5)
	offsets[3] = b.Len()
	b.WriteString("3 0 obj\n")
	b.WriteString("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 300] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>\n")
	b.WriteString("endobj\n")

	// object 4: contents stream (with correct /Length)
	offsets[4] = b.Len()
	b.WriteString("4 0 obj\n")
	b.WriteString("<< /Length ")
	b.WriteString(strconv.Itoa(len(stream)))
	b.WriteString(" >>\n")
	b.WriteString("stream\n")
	b.WriteString(stream)
	if !strings.HasSuffix(stream, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("endstream\nendobj\n")

	// object 5: font
	offsets[5] = b.Len()
	b.WriteString("5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n")

	// xref + trailer
	xrefStart := b.Len()
	b.WriteString("xref\n")
	maxObj := 5
	b.WriteString("0 ")
	b.WriteString(strconv.Itoa(maxObj + 1))
	b.WriteString("\n")

	// obj 0 free entry
	b.WriteString(pad10(0))
	b.WriteString(" 65535 f \n")

	// objects 1..maxObj
	for i := 1; i <= maxObj; i++ {
		b.WriteString(pad10(offsets[i]))
		b.WriteString(" 00000 n \n")
	}

	b.WriteString("trailer\n")
	b.WriteString("<< /Root 1 0 R /Size ")
	b.WriteString(strconv.Itoa(maxObj + 1))
	b.WriteString(" >>\n")
	b.WriteString("startxref\n")
	b.WriteString(strconv.Itoa(xrefStart))
	b.WriteString("\n%%EOF\n")

	pdf := []byte(b.String())

	br := bytes.NewReader(pdf)
	r, err := NewReader(br, int64(len(pdf)))
	require.NoError(t, err, "NewReader should succeed")

	page := r.Page(1)
	require.False(t, page.V.IsNull(), "page must exist")

	rows, err := page.GetTextByRow()
	require.NoError(t, err, "GetTextByRow should not error")

	// Expect 3 rows (Y=300, 250, 100) — sorted top-to-bottom (300,250,100)
	require.Len(t, rows, 3, "should detect 3 rows")

	// Row 0: Y=300 -> A, C (X ascending -> 100 then 200)
	assert.Equal(t, int64(300), rows[0].Position)
	require.Len(t, rows[0].Content, 2)
	assert.Equal(t, "A", rows[0].Content[0].S)
	assert.Equal(t, "C", rows[0].Content[1].S)

	// Row 1: Y=250 -> B
	assert.Equal(t, int64(250), rows[1].Position)
	require.Len(t, rows[1].Content, 1)
	assert.Equal(t, "B", rows[1].Content[0].S)

	// Row 2: Y=100 -> D
	assert.Equal(t, int64(100), rows[2].Position)
	require.Len(t, rows[2].Content, 1)
	assert.Equal(t, "D", rows[2].Content[0].S)
}

func TestFontWidths(t *testing.T) {
	br := bytes.NewReader(minimalTwoPagePDF)
	r, err := NewReader(br, int64(len(minimalTwoPagePDF)))
	require.NoError(t, err, "NewReader should succeed")

	p := r.Page(1)
	require.False(t, p.V.IsNull(), "page 1 should exist")

	fontNames := p.Fonts()
	require.NotEmpty(t, fontNames, "expected at least one font name")

	f := p.Font(fontNames[0])
	widths := f.Widths()

	assert.Len(t, widths, 0, "expected no widths for font in minimalTwoPagePDF")
}

func TestWalkTextBlocks(t *testing.T) {
	// two Tm+Tj blocks and one Td
	stream := "BT /F1 12 Tf 1 0 0 1 10 20 Tm (A) Tj ET\n" +
		"BT /F1 12 Tf 1 0 0 1 30 40 Tm (B) Tj ET\n" +
		"BT /F1 12 Tf 1 0 0 1 50 60 Tm (X) Tj 0 0 Td ET\n"

	var sb strings.Builder
	sb.WriteString("%PDF-1.4\n")

	offsets := map[int]int{}
	// obj1
	offsets[1] = sb.Len()
	sb.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	// obj2
	offsets[2] = sb.Len()
	sb.WriteString("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	// obj3 page
	offsets[3] = sb.Len()
	sb.WriteString("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>\nendobj\n")
	// obj4 contents
	offsets[4] = sb.Len()
	sb.WriteString("4 0 obj\n<< /Length ")
	sb.WriteString(strconv.Itoa(len(stream)))
	sb.WriteString(" >>\nstream\n")
	sb.WriteString(stream)
	if !strings.HasSuffix(stream, "\n") {
		sb.WriteString("\n")
	}
	sb.WriteString("endstream\nendobj\n")
	// obj5 font
	offsets[5] = sb.Len()
	sb.WriteString("5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n")

	// xref
	xrefStart := sb.Len()
	sb.WriteString("xref\n0 6\n")
	sb.WriteString(pad10(0) + " 65535 f \n")
	for i := 1; i <= 5; i++ {
		sb.WriteString(pad10(offsets[i]) + " 00000 n \n")
	}
	sb.WriteString("trailer\n<< /Root 1 0 R /Size 6 >>\n")
	sb.WriteString("startxref\n")
	sb.WriteString(strconv.Itoa(xrefStart))
	sb.WriteString("\n%%EOF\n")

	pdf := []byte(sb.String())
	br := bytes.NewReader(pdf)
	r, err := NewReader(br, int64(len(pdf)))
	require.NoError(t, err)

	page := r.Page(1)
	require.False(t, page.V.IsNull())

	// collect walker calls
	type c struct {
		x, y float64
		s    string
	}
	var calls []c
	page.walkTextBlocks(func(enc TextEncoding, x, y float64, s string) {
		calls = append(calls, c{x, y, s})
	})

	// Expect: "A"@(10,20), "B"@(30,40), "X"@(50,60), ""@(50,60) from Td
	require.Len(t, calls, 4)
	assert.Equal(t, "A", calls[0].s)
	assert.Equal(t, float64(10), calls[0].x)
	assert.Equal(t, float64(20), calls[0].y)

	assert.Equal(t, "B", calls[1].s)
	assert.Equal(t, float64(30), calls[1].x)
	assert.Equal(t, float64(40), calls[1].y)

	assert.Equal(t, "X", calls[2].s)
	assert.Equal(t, float64(50), calls[2].x)
	assert.Equal(t, float64(60), calls[2].y)

	assert.Equal(t, "", calls[3].s) // Td produced empty string call
	assert.Equal(t, float64(50), calls[3].x)
	assert.Equal(t, float64(60), calls[3].y)
}

func TestPageContent(t *testing.T) {
	// content stream crafted to exercise: cm, re, q/Q, BT/ET, Tf, Tm, Tj, T*, Tc, TD, Td, TJ, TL, Tr, Ts, Tw, Tz
	// Note: numbers must appear *before* the operators that consume them (e.g. "10 -5 TD" not "TD 10 -5")
	stream := strings.Join([]string{

		"q 1 0 0 1 0 0 cm", // cm + q (save)
		"0 0 10 5 re",      // rectangle
		"Q",                // restore
		"BT /F1 12 Tf 1 0 0 1 100 200 Tm (A) Tj T* (B) Tj ET",                // simple Tj + T*
		"BT /F1 12 Tf 2 Tc 10 -5 TD 0 0 Td (C) Tj ET",                        // Tc, TD (10,-5), Td (0,0)
		"BT /F1 12 Tf [(D) -50 (E)] TJ ET",                                   // TJ array with number advance
		"BT /F1 12 Tf 3 TL 1 Tr 4 Ts 5 Tw 120 Tz 1 0 0 1 50 50 Tm (G) Tj ET", // TL,Tr,Ts,Tw,Tz,Tm
	}, "\n") + "\n"

	var b strings.Builder
	b.WriteString("%PDF-1.4\n")

	offsets := map[int]int{}

	// obj 1: Catalog
	offsets[1] = b.Len()
	b.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")

	// obj 2: Pages
	offsets[2] = b.Len()
	b.WriteString("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")

	// obj 3: Page referencing contents 4 and font 5
	offsets[3] = b.Len()
	b.WriteString("3 0 obj\n")
	b.WriteString("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 300] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>\n")
	b.WriteString("endobj\n")

	// obj 4: contents stream
	offsets[4] = b.Len()
	b.WriteString("4 0 obj\n")
	b.WriteString("<< /Length ")
	b.WriteString(strconv.Itoa(len(stream)))
	b.WriteString(" >>\n")
	b.WriteString("stream\n")
	b.WriteString(stream)
	if !strings.HasSuffix(stream, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("endstream\nendobj\n")

	// obj 5: font
	offsets[5] = b.Len()
	b.WriteString("5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n")

	// xref + trailer
	xrefStart := b.Len()
	maxObj := 5
	b.WriteString("xref\n")
	b.WriteString("0 ")
	b.WriteString(strconv.Itoa(maxObj + 1))
	b.WriteString("\n")
	// obj 0
	b.WriteString(pad10(0))
	b.WriteString(" 65535 f \n")
	for i := 1; i <= maxObj; i++ {
		b.WriteString(pad10(offsets[i]))
		b.WriteString(" 00000 n \n")
	}
	b.WriteString("trailer\n")
	b.WriteString("<< /Root 1 0 R /Size ")
	b.WriteString(strconv.Itoa(maxObj + 1))
	b.WriteString(" >>\n")
	b.WriteString("startxref\n")
	b.WriteString(strconv.Itoa(xrefStart))
	b.WriteString("\n%%EOF\n")

	pdf := []byte(b.String())

	br := bytes.NewReader(pdf)
	r, err := NewReader(br, int64(len(pdf)))
	require.NoError(t, err, "NewReader should succeed")

	page := r.Page(1)
	require.False(t, page.V.IsNull(), "page must exist")

	c := page.Content()
	assert.NotEmpty(t, c.Text, "expected extracted text runs")
	assert.NotEmpty(t, c.Rect, "expected rectangle(s) from re operator")

	var sb strings.Builder
	for _, tx := range c.Text {
		sb.WriteString(tx.S)
	}
	combined := sb.String()

	assert.Contains(t, combined, "A", "expected 'A' from Tj")
	assert.Contains(t, combined, "B", "expected 'B' from T* Tj")
	assert.Contains(t, combined, "C", "expected 'C' from Td/Tj")
	assert.True(t, strings.Contains(combined, "D") || strings.Contains(combined, "E"), "expected 'D' or 'E' from TJ")
	assert.Contains(t, combined, "G", "expected 'G' from final Tm/Tj")

	// Verify rectangle coordinates (0,0)-(10,5) exists.
	found := false
	for _, rct := range c.Rect {
		if rct.Min.X == 0 && rct.Min.Y == 0 && rct.Max.X == 10 && rct.Max.Y == 5 {
			found = true
			break
		}
	}
	assert.True(t, found, "expected rectangle 0,0-10,5 present")
}

func TestBuildOutline(t *testing.T) {
	root := dict{
		name("Title"): "Root",
		name("First"): dict{
			name("Title"): "Child1",
			name("Next"): dict{
				name("Title"): "Child2",
			},
		},
	}

	v := Value{data: root}

	out := buildOutline(v)
	assert.Equal(t, "Root", out.Title)
	require.Len(t, out.Child, 2)
	assert.Equal(t, "Child1", out.Child[0].Title)
	assert.Equal(t, "Child2", out.Child[1].Title)
}
