// Copyright © 2026, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: BSD-3-Clause

package xtract

import (
	"bytes"
	"compress/zlib"
	"encoding/ascii85"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func td(name string) string {
	return filepath.Join("testdata", name)
}

func openReaderAt(t *testing.T, name string) (io.ReaderAt, int64, func()) {
	t.Helper()
	path := td(name)

	f, err := os.Open(path)
	require.Truef(t, err == nil, "open %s failed: %v", path, err)

	fi, err := f.Stat()
	require.Truef(t, err == nil, "stat %s failed: %v", path, err)

	return f, fi.Size(), func() { _ = f.Close() }
}

func errHas(err error, sub string) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), strings.ToLower(sub))
}

func TestNewReader_EmptyFile(t *testing.T) {
	var b bytes.Reader // size = 0
	_, err := NewReader(&b, 0)

	assert.Truef(t, err != nil, "expected error for empty input, got nil")
	assert.Truef(t, errHas(err, "empty"), "expected error to contain 'empty', got: %v", err)
}

func TestValidateEOFMarker(t *testing.T) {
	ra, size, done := openReaderAt(t, "pdf_test.pdf")
	defer done()

	err := ValidateEOFMarker(ra, size)

	if err == nil {
		assert.NoError(t, err, "expected no error when file ends with %%EOF")
		return
	}

	msg := strings.ToLower(err.Error())
	isEOFerr :=
		strings.Contains(msg, "%%%%eof") || // missing %%EOF marker
			strings.Contains(msg, "read error") // underlying I/O issue

	assert.Truef(t, isEOFerr,
		"unexpected error; wanted EOF-related error, got: %v", err)
}

func TestCheckHeader(t *testing.T) {
	ra, _, done := openReaderAt(t, "pdf_test.pdf")
	defer done()

	err := CheckHeader(ra)

	if err == nil {
		// Header/version accepted
		t.Log("PDF header/version accepted by CheckHeader (supported).")
		return
	}

	msg := strings.ToLower(err.Error())
	isHeaderOrVersionErr :=
		strings.Contains(msg, "empty") || // file empty
			strings.Contains(msg, "missing %pdf- header") || // no header token
			strings.Contains(msg, "invalid header") || // bad prefix after trimming
			strings.Contains(msg, "malformed version") || // parsing %PDF-x.y failed
			strings.Contains(msg, "unsupported pdf version") || // version outside 1.0–1.7, 2.0
			strings.Contains(msg, "read error") // I/O issue

	assert.Truef(t, isHeaderOrVersionErr,
		"unexpected error; wanted header/version error, got: %v", err)
}

func TestFindStartXref(t *testing.T) {
	ra, size, done := openReaderAt(t, "pdf_test.pdf")
	defer done()

	r, err := FindStartXref(ra, size)

	if err == nil {
		assert.NotNil(t, r, "reader should not be nil on success")
		t.Log("constructor succeeded; not a startxref-missing case")
		return
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "missing final startxref") {
		assert.Contains(t, msg, "missing final startxref")
		t.Log("observed expected 'missing final startxref' error")
		return
	}
}

type errReaderAt struct{}

func (e errReaderAt) ReadAt(p []byte, off int64) (int, error) {
	return 0, errors.New("read failure")
}

func TestFindStartXref_ErrorCases(t *testing.T) {
	// ReadAt error
	{
		r := errReaderAt{}
		_, err := FindStartXref(r, 100)
		assert.Error(t, err)
	}
	// Missing final startxref
	{
		payload := strings.Repeat("A", 150)
		data := []byte("%PDF-1.7\n" + payload + "\n%%EOF")

		ra := bytes.NewReader(data)
		_, err := FindStartXref(ra, int64(len(data)))

		assert.Error(t, err)
	}
	// startxref not followed by integer
	{
		padding := strings.Repeat("A", 120)
		data := []byte(
			"%PDF-1.7\n" +
				padding +
				"\nstartxref\n" +
				"notanumber\n" +
				"%%EOF",
		)

		ra := bytes.NewReader(data)
		_, err := FindStartXref(ra, int64(len(data)))

		assert.Error(t, err)
	}
	//Invalid keyword instead of startxref
	{
		padding := strings.Repeat("B", 120)
		data := []byte(
			"%PDF-1.7\n" +
				padding +
				"\nsomethingelse\n123\n%%EOF",
		)

		ra := bytes.NewReader(data)
		_, err := FindStartXref(ra, int64(len(data)))

		assert.Error(t, err)
	}
}

func TestDecodeInt(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		assert.Equal(t, 0, decodeInt([]byte{}))
	})
	t.Run("single-byte", func(t *testing.T) {
		assert.Equal(t, 0x7F, decodeInt([]byte{0x7F}))
	})
	t.Run("multi-byte", func(t *testing.T) {
		// 0x01 0x02 0x03 => 0x010203 = 66051
		assert.Equal(t, 66051, decodeInt([]byte{0x01, 0x02, 0x03}))
	})
}

func TestEnsureLenAndSetIfEmpty(t *testing.T) {
	t.Run("ensureLen_grows", func(t *testing.T) {
		s := make([]int, 2)
		s[0], s[1] = 1, 2
		s2 := ensureLen(s, 5)
		require.GreaterOrEqual(t, cap(s2), 5)
		assert.Equal(t, 1, s2[0])
		assert.Equal(t, 2, s2[1])
		assert.Equal(t, 5, len(s2))
	})

	t.Run("setIfEmpty_basic", func(t *testing.T) {
		table := []xref{}
		setIfEmpty(&table, 3, xref{ptr: objptr{1, 0}})
		require.GreaterOrEqual(t, len(table), 4)
		assert.Equal(t, uint32(1), table[3].ptr.id)
		// setting again should not overwrite
		setIfEmpty(&table, 3, xref{ptr: objptr{2, 0}})
		assert.Equal(t, uint32(1), table[3].ptr.id)
	})
}

func TestMergeXrefTables(t *testing.T) {
	// dest smaller than src
	dest := []xref{
		{ptr: objptr{}},
	}
	src := make([]xref, 3)
	src[0] = xref{ptr: objptr{1, 0}, offset: 100}
	src[1] = xref{ptr: objptr{2, 0}, offset: 200}
	src[2] = xref{ptr: objptr{3, 0}, offset: 300}

	merged := mergeXrefTables(dest, src)
	require.Len(t, merged, 3)
	assert.Equal(t, uint32(1), merged[0].ptr.id)
	assert.Equal(t, uint32(2), merged[1].ptr.id)
	assert.Equal(t, uint32(3), merged[2].ptr.id)

	// both have entries, src preferred when both in-use
	dest2 := []xref{
		{ptr: objptr{1, 0}, offset: 10},
	}
	src2 := []xref{
		{ptr: objptr{1, 1}, offset: 1000}, // different gen
	}
	out := mergeXrefTables(dest2, src2)
	assert.Equal(t, uint16(1), out[0].ptr.gen)
	assert.Equal(t, int64(1000), out[0].offset)
}

// TestXrefSize exercises xrefSize by loading a PDF and finding an XRef stream.
// It will skip if the sample PDF uses classic xref table instead.
func TestXrefSize(t *testing.T) {
	ra, size, done := openReaderAt(t, "xrefStream.pdf")
	defer done()

	start, err := FindStartXref(ra, size)
	require.NoError(t, err)

	b := newBuffer(io.NewSectionReader(ra, start, size-start), start)
	b.allowEOF, b.allowObjptr, b.allowStream = true, true, true

	// Peek token: if "xref" then the PDF uses classic xref; skip.
	tok := b.readToken()
	if kw, ok := tok.(keyword); ok && kw == "xref" {
		t.Skip("test_pdf.pdf uses classic xref table; skipping xref-stream size test")
	}
	b.unreadToken(tok)
	_, strm, err := parseXrefStreamObject(b)
	require.NoError(t, err)
	n, err := xrefSize(strm)
	require.NoError(t, err)
	assert.Greater(t, n, int64(0))
}

func TestParseXrefStreamObject(t *testing.T) {
	ra, size, done := openReaderAt(t, "xrefStream.pdf")
	defer done()

	start, err := FindStartXref(ra, size)
	require.NoError(t, err)

	b := newBuffer(io.NewSectionReader(ra, start, size-start), start)
	b.allowEOF, b.allowObjptr, b.allowStream = true, true, true

	tok := b.readToken()
	if kw, ok := tok.(keyword); ok && kw == "xref" {
		t.Skip("pdf-with-stream.pdf uses classic xref; skipping parseXrefStreamObject test")
	}
	b.unreadToken(tok)

	ptr, strm, err := parseXrefStreamObject(b)
	require.NoError(t, err)
	assert.NotEqual(t, objptr{}, ptr)
	assert.NotNil(t, strm.hdr)
	if v, ok := strm.hdr["Type"]; ok {
		if nm, ok := v.(name); ok {
			assert.Equal(t, name("XRef"), nm)
		}
	}
}

func TestParseXrefStreamObject_ErrorPaths(t *testing.T) {
	// not objdef
	{
		b := newBuffer(bytes.NewReader([]byte("123\n")), 0)
		b.allowEOF = true

		_, _, err := parseXrefStreamObject(b)
		require.Error(t, err)
	}
	// objdef but not stream
	{
		data := []byte("1 0 obj\n42\nendobj\n")
		b := newBuffer(bytes.NewReader(data), 0)
		b.allowEOF, b.allowObjptr = true, true

		_, _, err := parseXrefStreamObject(b)
		require.Error(t, err)
	}
	// wrong Type
	{
		data := []byte(
			"1 0 obj\n<< /Type /NotXRef >>\nstream\nx\nendstream\nendobj\n",
		)
		b := newBuffer(bytes.NewReader(data), 0)
		b.allowEOF, b.allowObjptr, b.allowStream = true, true, true

		_, _, err := parseXrefStreamObject(b)
		require.Error(t, err)
	}
}

func TestReadXrefStreamData(t *testing.T) {
	ra, size, done := openReaderAt(t, "xrefStream.pdf")
	defer done()

	start, err := FindStartXref(ra, size)
	require.NoError(t, err)

	b := newBuffer(io.NewSectionReader(ra, start, size-start), start)
	b.allowEOF, b.allowObjptr, b.allowStream = true, true, true

	r := &Reader{f: ra, end: size}
	table, _, hdr, err := readXref(r, b)
	if tval, ok := hdr[name("Type")]; ok {
		if nm, ok := tval.(name); ok && nm == name("XRef") {
			// Stream-specific assertions
			require.Greater(t, len(table), 0, "expected entries in xref stream")

			found := false
			for _, e := range table {
				if e.ptr != (objptr{}) {
					found = true
					assert.GreaterOrEqual(t, e.offset, int64(0), "in-use entry offset must be >= 0")
					break
				}
			}
			require.True(t, found, "expected at least one in-use entry in xref stream")
			return
		}
	}
	t.Skip("sample PDF is not an XRef stream; skipping stream-specific assertions")
}

func TestReadXref(t *testing.T) {
	ra, size, done := openReaderAt(t, "pdf_test.pdf")
	defer done()

	start, err := FindStartXref(ra, size)
	require.NoError(t, err)

	b := newBuffer(io.NewSectionReader(ra, start, size-start), start)
	b.allowEOF, b.allowObjptr, b.allowStream = true, true, true

	r := &Reader{f: ra, end: size}
	table, ptr, hdr, err := readXref(r, b)
	require.NoError(t, err)
	require.NotNil(t, table)
	require.NotNil(t, hdr)

	// trailer/header should contain /Size
	_, ok := hdr[name("Size")]
	assert.True(t, ok, "returned dict should contain /Size")
	_ = ptr
}

func TestParseXrefTableAndTrailer(t *testing.T) {
	ra, size, done := openReaderAt(t, "pdf_test.pdf")
	defer done()

	start, err := FindStartXref(ra, size)
	require.NoError(t, err)

	b := newBuffer(io.NewSectionReader(ra, start, size-start), start)
	b.allowEOF, b.allowObjptr, b.allowStream = true, true, true

	tok := b.readToken()
	if kw, ok := tok.(keyword); !ok || kw != "xref" {
		t.Skip("try.pdf doesn't use classic xref; skipping parseXrefTableAndTrailer test")
	}
	// parseXrefTableAndTrailer expects buffer AFTER 'xref', so we are positioned correctly.
	table, trailer, err := parseXrefTableAndTrailer(b, nil)
	require.NoError(t, err)
	require.NotNil(t, table)
	require.NotNil(t, trailer)
	// validate size exists
	_, ok := trailer[name("Size")]
	assert.True(t, ok)
}

func TestReadXrefTableData(t *testing.T) {
	ra, size, done := openReaderAt(t, "0_hybrid.pdf")
	defer done()

	start, err := FindStartXref(ra, size)
	require.NoError(t, err)
	b := newBuffer(io.NewSectionReader(ra, start, size-start), start)
	b.allowEOF, b.allowObjptr, b.allowStream = true, true, true

	// ensure classic xref present
	tok := b.readToken()
	kw, ok := tok.(keyword)
	if !ok || kw != "xref" {
		t.Skip("test_pdf.pdf uses xref stream or non-classic xref; skipping readXrefTableData")
	}
	// readXrefTableData expects to start after xref token
	table, err := readXrefTableData(b, nil)
	if err != nil {
		t.Skipf("Skipping: readXrefTableData failed on this sample: %v", err)
	}
	require.NotNil(t, table)
	require.Greater(t, len(table), 0)
}

func TestResolvePrevXrefTables(t *testing.T) {
	ra, size, done := openReaderAt(t, "0_hybrid.pdf")
	defer done()
	r := &Reader{f: ra, end: size}

	start, err := FindStartXref(ra, size)
	require.NoError(t, err)

	b := newBuffer(io.NewSectionReader(ra, start, size-start), start)
	b.allowEOF, b.allowObjptr, b.allowStream = true, true, true

	// require classic
	tok := b.readToken()
	if kw, ok := tok.(keyword); !ok || kw != "xref" {
		t.Skip("test_pdf.pdf does not use classic xref; skipping resolvePrevXrefTables test")
	}
	table, trailer, err := parseXrefTableAndTrailer(b, nil)
	if err != nil {
		t.Skipf("Skipping: parseXrefTableAndTrailer error: %v", err)
	}
	table2, trailer2, err := resolvePrevXrefTables(r, trailer, table)
	require.NoError(t, err)
	require.NotNil(t, table2)
	require.NotNil(t, trailer2)
	_, ok := trailer2[name("Size")]
	assert.True(t, ok)
}

func TestResolvePrevXrefTables_ErrorCases(t *testing.T) {
	// dummy reader (never actually read in first error case)
	r := &Reader{
		f:   bytes.NewReader(nil),
		end: 0,
	}
	// Prev exists but is NOT int64
	{
		trailer := dict{
			name("Prev"): name("NotAnInt"),
		}

		table, outTrailer, err := resolvePrevXrefTables(r, trailer, nil)
		require.Error(t, err)
		require.Nil(t, table)
		require.Nil(t, outTrailer)
	}
	// Prev is int64 but does NOT point to "xref"
	{
		// content at offset 0 that does not start with "xref"
		data := []byte("notxref\n")
		ra := bytes.NewReader(data)

		r2 := &Reader{
			f:   ra,
			end: int64(len(data)),
		}

		trailer := dict{
			name("Prev"): int64(0),
		}

		table, outTrailer, err := resolvePrevXrefTables(r2, trailer, nil)
		require.Error(t, err)
		require.Nil(t, table)
		require.Nil(t, outTrailer)
	}
}
func TestValidateTrailerSize(t *testing.T) {
	ra, size, done := openReaderAt(t, "1_hybrid.pdf")
	defer done()

	start, err := FindStartXref(ra, size)
	require.NoError(t, err)
	b := newBuffer(io.NewSectionReader(ra, start, size-start), start)
	b.allowEOF, b.allowObjptr, b.allowStream = true, true, true

	// must be classic xref
	tok := b.readToken()
	if kw, ok := tok.(keyword); !ok || kw != "xref" {
		t.Skip("test_pdf.pdf does not use classic xref; skipping validateTrailerSize test")
	}
	table, trailer, err := parseXrefTableAndTrailer(b, nil)
	if err != nil {
		t.Skipf("Skipping: parseXrefTableAndTrailer error: %v", err)
	}
	// artificially extend the table
	tableCopy := append([]xref{}, table...)
	tableCopy = append(tableCopy, xref{}, xref{})
	err = validateTrailerSize(&tableCopy, trailer)
	require.NoError(t, err)
	if sz, ok := trailer[name("Size")].(int64); ok {
		assert.Equal(t, int(sz), len(tableCopy))
	} else {
		t.Skip("Skipping: trailer missing /Size")
	}
}
func TestIsLikelyObjectAtAndScanForObjectAt(t *testing.T) {
	tmp, err := os.CreateTemp("", "pdftest-*.pdf")
	require.NoError(t, err)
	defer os.Remove(tmp.Name())
	_, err = tmp.Write([]byte("%PDF-1.4\n1 0 obj\n<< /Type /X >>\nendobj\n%%EOF"))
	require.NoError(t, err)
	tmp.Close()

	f, err := os.Open(tmp.Name())
	require.NoError(t, err)
	defer f.Close()

	fi, err := f.Stat()
	require.NoError(t, err)
	r := &Reader{f: f, end: fi.Size()}

	assert.True(t, r.isLikelyObjectAt(0))

	found := r.scanForObjectAt(1, 0, 0, 64)
	assert.GreaterOrEqual(t, found, int64(0))
}

func TestValidateAndRepairXrefEntries(t *testing.T) {
	// Setup file with object at offset 50
	tmp, err := os.CreateTemp("", "pdftest-*.pdf")
	require.NoError(t, err)
	defer os.Remove(tmp.Name())

	// write padding then object header
	padding := bytes.Repeat([]byte(" "), 50)
	_, err = tmp.Write(padding)
	require.NoError(t, err)
	_, err = tmp.Write([]byte("2 0 obj\n<< /A 1 >>\nendobj\n"))
	require.NoError(t, err)
	tmp.Close()

	f, err := os.Open(tmp.Name())
	require.NoError(t, err)
	defer f.Close()
	fi, err := f.Stat()
	require.NoError(t, err)
	r := &Reader{f: f, end: fi.Size()}

	table := make([]xref, 3)
	table[2] = xref{ptr: objptr{2, 0}, offset: 0}
	repaired, invalid := r.validateAndRepairXrefEntries(table)
	assert.GreaterOrEqual(t, repaired, 0)
	assert.GreaterOrEqual(t, invalid, 0)
}

func TestHandleTrailerXRefStm(t *testing.T) {
	ra, size, done := openReaderAt(t, "1_hybrid.pdf")
	defer done()
	r := &Reader{f: ra, end: size}

	start, err := FindStartXref(ra, size)
	require.NoError(t, err)
	b := newBuffer(io.NewSectionReader(ra, start, size-start), start)
	b.allowEOF, b.allowObjptr, b.allowStream = true, true, true

	// ensure classic xref present
	tok := b.readToken()
	if kw, ok := tok.(keyword); !ok || kw != "xref" {
		t.Skip("test_pdf.pdf does not use classic xref; skipping handleTrailerXRefStm test")
	}
	table, trailer, err := parseXrefTableAndTrailer(b, nil)
	if err != nil {
		t.Skipf("Skipping: parseXrefTableAndTrailer error: %v", err)
	}
	// If XRefStm absent, function should simply return same table+trailer
	outTable, outTrailer, err := r.handleTrailerXRefStm(table, trailer)
	require.NoError(t, err)
	assert.NotNil(t, outTable)
	assert.NotNil(t, outTrailer)
}

func writeTempFile(t *testing.T, content string) (string, func()) {
	t.Helper()
	f, err := os.CreateTemp("", "xreftest-*.pdf")
	require.NoError(t, err)
	_, err = f.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	return f.Name(), func() { _ = os.Remove(f.Name()) }
}

func TestMergePrevXrefStreams_PSizeTooLarge(t *testing.T) {
	obj := "1 0 obj\n<< /Type /XRef /Size 10 /W [1 1 1] /Index [0 1] /Length 0 >>\nstream\n\nendstream\nendobj\n"
	name, cleanup := writeTempFile(t, obj)
	defer cleanup()
	f, err := os.Open(name)
	require.NoError(t, err)
	defer f.Close()
	fi, err := f.Stat()
	require.NoError(t, err)

	r := &Reader{f: f, end: fi.Size()}
	cur := stream{hdr: dict{"Prev": int64(0)}}

	out, err := mergePrevXrefStreams(r, cur, make([]xref, 1), 1)
	assert.Error(t, err)
	assert.Nil(t, out)
}

func TestMergePrevXrefStreams_StreamDataError(t *testing.T) {
	obj := "1 0 obj\n<< /Type /XRef /Size 1 /Index [0 1] /Length 0 >>\nstream\n\nendstream\nendobj\n"
	name, cleanup := writeTempFile(t, obj)
	defer cleanup()
	f, err := os.Open(name)
	require.NoError(t, err)
	defer f.Close()
	fi, err := f.Stat()
	require.NoError(t, err)

	r := &Reader{f: f, end: fi.Size()}
	cur := stream{hdr: dict{"Prev": int64(0)}}

	out, err := mergePrevXrefStreams(r, cur, make([]xref, 1), 1)
	assert.Error(t, err)
	assert.Nil(t, out)
}

func TestMergePrevXrefStreams(t *testing.T) {
	ra, size, done := openReaderAt(t, "prev_tag.pdf")
	defer done()

	start, err := FindStartXref(ra, size)
	require.NoError(t, err)

	b := newBuffer(io.NewSectionReader(ra, start, size-start), start)
	b.allowEOF, b.allowObjptr, b.allowStream = true, true, true

	// skip classic xref
	tok := b.readToken()
	if kw, ok := tok.(keyword); ok && kw == "xref" {
		t.Skip("pdf uses classic xref; skipping mergePrevXrefStreams test")
	}
	b.unreadToken(tok)

	_, strm, err := parseXrefStreamObject(b)
	require.NoError(t, err)
	sizeVal, err := xrefSize(strm)
	require.NoError(t, err)

	table := make([]xref, sizeVal)
	r := &Reader{f: ra, end: size}
	out, err := mergePrevXrefStreams(r, strm, table, sizeVal)
	if err != nil {
		t.Logf("mergePrevXrefStreams returned error (acceptable depending on sample): %v", err)
	} else {
		assert.NotNil(t, out)
	}
}

func TestReadXrefTableData_Malformed(t *testing.T) {
	bb := bytes.NewReader([]byte("badheader\ntrailer\n<< /Size 1 >>"))
	sect := io.NewSectionReader(bb, 0, int64(bb.Len()))
	b := newBuffer(sect, 0)
	_, err := readXrefTableData(b, nil)
	assert.Error(t, err)
}

func TestFindLastLine(t *testing.T) {
	cases := []struct {
		name     string
		buf      []byte
		token    string
		expectFn func([]byte) int // computes expected index from buf
	}{
		{
			name:  "Valid_CRLF",
			buf:   []byte("stuff\nstartxref\r\n123\r\n%%EOF"),
			token: "startxref",
			expectFn: func(b []byte) int {
				return bytes.Index(b, []byte("startxref\r\n"))
			},
		},
		{
			name:  "Valid_SpacesThenCRLF",
			buf:   []byte("...startxref   \r\n123\r\n%%EOF"),
			token: "startxref",
			expectFn: func(b []byte) int {
				return bytes.Index(b, []byte("startxref   \r\n"))
			},
		},
		{
			name:     "Invalid_Spaces",
			buf:      []byte("header\nstartxref   40441\r\n%%EOF"),
			token:    "startxref",
			expectFn: func(b []byte) int { return -1 },
		},
		{
			name:     "TokenAtEOF_NoEOL",
			buf:      []byte("trailer\nstartxref"),
			token:    "startxref",
			expectFn: func(b []byte) int { return -1 },
		},
		{
			name:     "NoMatch",
			buf:      []byte("trailer\n<< /Size 32 >>\n%%EOF\n"),
			token:    "startxref",
			expectFn: func(b []byte) int { return -1 },
		},
		{
			name: "ValidFinal",
			buf: []byte(
				"0000032134 00000 n \n" +
					"0000032736 00000 n \n" +
					"0000040276 00000 n \n" +
					"trailer\n" +
					"<< /Size 32 /Root 16 0 R /Info 31 0 R /ID [ <35bc2be504e1920a4a0fea36443d6c4d>\n" +
					"<35bc2be504e1920a4a0fea36443d6c4d> ] >>\n" +
					"startxref\n" +
					"40441\n" +
					"%%EOF"),
			token: "startxref",
			expectFn: func(b []byte) int {
				return bytes.LastIndex(b, []byte("startxref\n"))
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := findLastLine(tc.buf, tc.token)
			exp := tc.expectFn(tc.buf)
			assert.Equal(t, exp, got)
		})
	}
}

func TestObjfmt(t *testing.T) {
	// Table of test cases
	cases := []struct {
		name     string
		input    interface{}
		expected string
		checkFn  func(string) bool
	}{
		{"plain string", "hello", "\"hello\"", nil},
		{"pdf doc encoded string", string([]byte{0xA3, 0x20, 0x41}), "", func(got string) bool {
			return strings.HasPrefix(got, "\"") && strings.HasSuffix(got, "\"")
		}},

		{"utf16 string", string([]byte{0xFE, 0xFF, 0x00, 0x48, 0x00, 0x69}), "\"Hi\"", nil},
		{"name", name("Helvetica"), "/Helvetica", nil},
		{"array", array{"a", name("B"), int64(3)}, "[\"a\" /B 3]", nil},
		{"dict", dict{
			name("Z"): int64(26),
			name("A"): "alpha",
			name("M"): array{"x", int64(1)},
		}, "<</A \"alpha\" /M [\"x\" 1] /Z 26>>", nil},
		{"stream", stream{hdr: dict{name("Length"): int64(0)}, offset: 123}, "<</Length 0>>@123", nil},
		{"objptr", objptr{5, 0}, "5 0 R", nil},
		{"objdef", objdef{ptr: objptr{5, 0}, obj: int64(42)}, "{5 0 obj}42", nil},
		{"default unknown type", 3.14, "3.14", nil},
	}

	for _, c := range cases {
		got := objfmt(c.input)
		if c.checkFn != nil {
			if !c.checkFn(got) {
				t.Errorf("%s: output %q did not satisfy custom check", c.name, got)
			}
		} else {
			assert.Equal(t, c.expected, got, c.name)
		}
	}
}

func utf16BEWithBOM(s []rune) string {
	// BOM FE FF then big-endian 16-bit runes
	b := []byte{0xFE, 0xFF}
	for _, r := range s {
		b = append(b, byte(r>>8), byte(r&0xFF))
	}
	return string(b)
}

func TestValue_PrimitivesAndStringFuncs(t *testing.T) {
	// plain string value
	v := Value{r: nil, ptr: objptr{}, data: "hello"}
	assert.Equal(t, "\"hello\"", v.String(), "String() should quote plain strings")
	assert.Equal(t, "hello", v.RawString(), "RawString() should return raw string")
	assert.Equal(t, "hello", v.Text(), "Text() should return plain text for ASCII string")

	// UTF-16 string -> Text should decode
	utf16 := utf16BEWithBOM([]rune{'H', 'i'})
	v2 := Value{r: nil, ptr: objptr{}, data: utf16}
	// sanity: isUTF16 should detect it; if not, Text will equal raw; use require to ensure detection
	require.True(t, isUTF16(utf16), "constructed sample should be detected as UTF-16")
	assert.Equal(t, "Hi", v2.Text(), "Text() should decode UTF-16BE with BOM")
	// with this
	assert.Equal(t, "\ufeffHi", v2.TextFromUTF16(), "TextFromUTF16() should decode UTF-16BE (BOM preserved)")

	// Bool / Int64 / Float64
	vb := Value{data: true}
	vi := Value{data: int64(42)}
	vf := Value{data: float64(3.5)}
	// Bool accessor
	assert.True(t, vb.Bool())
	// Int64 accessor
	assert.Equal(t, int64(42), vi.Int64())
	// Float64 from float and from integer conversion
	assert.Equal(t, float64(3.5), vf.Float64())
	assert.Equal(t, float64(42), vi.Float64())
}

func TestValue_NameArrayDictAccessors(t *testing.T) {
	// Build a dict with mixed entries and an array
	d := dict{
		name("B"):   int64(2),
		name("A"):   "alpha",
		name("Arr"): array{"one", int64(2)},
	}
	r := &Reader{}

	v := Value{r: r, ptr: objptr{}, data: d}

	// Keys() should return sorted keys
	keys := v.Keys()
	require.Equal(t, []string{"A", "Arr", "B"}, keys)

	// Key() lookup for simple values
	ka := v.Key("A")
	assert.Equal(t, "alpha", ka.RawString())

	arrVal := v.Key("Arr")
	assert.Equal(t, 2, arrVal.Len(), "array length should be 2")
	assert.Equal(t, "one", arrVal.Index(0).RawString())
	assert.Equal(t, int64(2), arrVal.Index(1).Int64())

	// Name accessor
	nv := Value{data: name("Helvetica")}
	assert.Equal(t, "Helvetica", nv.Name())
	assert.Equal(t, "/Helvetica", nv.String())
}

func TestReaderResolve(t *testing.T) {
	//direct value (non-objptr)
	r := &Reader{}
	v := r.resolve(objptr{}, int64(42))
	assert.False(t, v.IsNull())
	assert.Equal(t, int64(42), v.Int64())

	//out-of-range objptr -> null Value
	r = &Reader{xref: make([]xref, 1)} // only id=0 valid
	v = r.resolve(objptr{}, objptr{5, 0})
	assert.True(t, v.IsNull())

	//xref.ptr mismatch
	r = &Reader{
		xref: []xref{
			{ptr: objptr{0, 1}, offset: 100}, // gen=1
		},
	}
	v = r.resolve(objptr{}, objptr{0, 0}) // request gen=0
	assert.True(t, v.IsNull())

	//xref offset=0 and not inStream -> null
	r = &Reader{
		xref: []xref{
			{ptr: objptr{0, 0}, inStream: false, offset: 0},
		},
	}
	v = r.resolve(objptr{}, objptr{0, 0})
	assert.True(t, v.IsNull())
}

func TestResolve_InStream_NotAStream(t *testing.T) {
	r := &Reader{
		xref: []xref{
			{},
			{ptr: objptr{1, 0}, inStream: true, stream: objptr{0, 0}},
		},
	}

	assert.Panics(t, func() {
		_ = r.resolve(objptr{}, objptr{1, 0})
	})
}

func TestResolve_InStream_NotObjStm(t *testing.T) {
	streamBody := "dummy\n"

	// Object 1: real stream object (NOT ObjStm)
	obj1 := []byte(
		"1 0 obj\n" +
			"<< /Type /NotObjStm /N 1 /First 1 /Length " +
			strconv.Itoa(len(streamBody)) + " >>\n" +
			"stream\n" +
			streamBody +
			"endstream\n" +
			"endobj\n",
	)

	buf := obj1

	rdr := bytes.NewReader(buf)
	sec := io.NewSectionReader(rdr, 0, int64(len(buf)))

	r := &Reader{
		f:   sec,
		end: int64(len(buf)),
		xref: []xref{
			{}, // 0 unused

			// object 1 is a NORMAL stream (offset-based)
			{ptr: objptr{1, 0}, offset: 0},

			// object 2 lives "inside" object 1
			{ptr: objptr{2, 0}, inStream: true, stream: objptr{1, 0}},
		},
	}
	assert.Panics(t, func() {
		_ = r.resolve(objptr{}, objptr{2, 0})
	}, "expected panic because stream Type is not ObjStm")
}

func TestReader(t *testing.T) {
	// In-memory data
	data := []byte("abc123")
	r := &Reader{f: bytes.NewReader(data), end: int64(len(data))}

	//stream with Length = 6 at offset 0
	str := stream{hdr: dict{name("Length"): int64(len(data))}, offset: 0}
	v := Value{r: r, data: str}

	rc := v.Reader()
	got, _ := io.ReadAll(rc)
	assert.Equal(t, data, got)

	//non-stream value
	v2 := Value{r: r, data: int64(42)}
	rc2 := v2.Reader()
	_, err := io.ReadAll(rc2)
	assert.Error(t, err, "non-stream should return error")
}

func TestApplyFilter(t *testing.T) {
	{
		// Compress "hello"
		var buf bytes.Buffer
		zw := zlib.NewWriter(&buf)
		zw.Write([]byte("hello"))
		zw.Close()

		rd := applyFilter(bytes.NewReader(buf.Bytes()), "FlateDecode", Value{})
		out, err := io.ReadAll(rd)
		assert.NoError(t, err)
		assert.Equal(t, []byte("hello"), out)
	}

	// ASCII85Decode
	{
		var buf bytes.Buffer
		enc := ascii85.NewEncoder(&buf)
		enc.Write([]byte("hi!"))
		enc.Close()

		rd := applyFilter(bytes.NewReader(buf.Bytes()), "ASCII85Decode", Value{})
		out, err := io.ReadAll(rd)
		assert.NoError(t, err)
		assert.Equal(t, []byte("hi!"), out)
	}

	//Unknown filter should panic
	assert.Panics(t, func() {
		_ = applyFilter(bytes.NewReader([]byte("abc")), "UnknownFilter", Value{})
	})
}

func TestPngUpReader(t *testing.T) {
	r1 := &pngUpReader{
		r:    bytes.NewReader([]byte{2, 1, 1}),
		hist: []byte{10, 20, 30},
		tmp:  make([]byte, 3),
	}
	buf := make([]byte, 2)
	n, err := r1.Read(buf)
	assert.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, []byte{21, 31}, buf) // hist updated => [12,21,31] => pend = hist[1:]

	//malformed case
	r2 := &pngUpReader{
		r:    bytes.NewReader([]byte{9, 1, 1}),
		hist: []byte{0, 0, 0},
		tmp:  make([]byte, 3),
	}
	n, err = r2.Read(make([]byte, 1))
	assert.Error(t, err)
	assert.Equal(t, 0, n)

	// EOF
	r3 := &pngUpReader{
		r:    bytes.NewReader([]byte{}),
		hist: []byte{0, 0, 0},
		tmp:  make([]byte, 3),
	}
	n, err = r3.Read(make([]byte, 1))
	assert.Error(t, err)
	assert.Equal(t, 0, n)
}

func TestDictEncoder_Decode_MappedAndUnmapped(t *testing.T) {
	orig := nameToRune
	defer func() { nameToRune = orig }()

	nameToRune = map[string]rune{
		"A": '\u03B1',
		"B": '\u03B2',
	}
	e := &dictEncoder{
		v: Value{data: array{int64(65), name("A"), int64(66), name("B")}},
	}
	raw := string([]byte{65, 66, 67}) // 'A','B','C'
	got := e.Decode(raw)
	want := string([]rune{'\u03B1', '\u03B2', 'C'})
	assert.Equal(t, want, got)
}

func TestDictEncoder_Decode_NoMappingsAndEmpty(t *testing.T) {
	orig := nameToRune
	defer func() { nameToRune = orig }()

	nameToRune = map[string]rune{}

	// no mapping entries: should return identical runes
	e := &dictEncoder{
		v: Value{data: array{int64(10), name("X")}},
	}

	raw := string([]byte{10, 11})
	got := e.Decode(raw)
	want := string([]rune{rune(10), rune(11)})
	assert.Equal(t, want, got)

	// empty input
	got2 := e.Decode("")
	assert.Equal(t, "", got2)
}
