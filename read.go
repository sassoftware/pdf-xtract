// Copyright © 2026, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: BSD-3-Clause

// Package pdf implements reading of PDF files.
//
// # Overview
//
// PDF is Adobe's Portable Document Format, ubiquitous on the internet.
// A PDF document is a complex data format built on a fairly simple structure.
// This package exposes the simple structure along with some wrappers to
// extract basic information. If more complex information is needed, it is
// possible to extract that information by interpreting the structure exposed
// by this package.
//
// Specifically, a PDF is a data structure built from Values, each of which has
// one of the following Kinds:
//
//	Null, for the null object.
//	Integer, for an integer.
//	Real, for a floating-point number.
//	Bool, for a boolean value.
//	Name, for a name constant (as in /Helvetica).
//	String, for a string constant.
//	Dict, for a dictionary of name-value pairs.
//	Array, for an array of values.
//	Stream, for an opaque data stream and associated header dictionary.
//
// The accessors on Value—Int64, Float64, Bool, Name, and so on—return
// a view of the data as the given type. When there is no appropriate view,
// the accessor returns a zero result. For example, the Name accessor returns
// the empty string if called on a Value v for which v.Kind() != Name.
// Returning zero values this way, especially from the Dict and Array accessors,
// which themselves return Values, makes it possible to traverse a PDF quickly
// without writing any error checking. On the other hand, it means that mistakes
// can go unreported.
//
// The basic structure of the PDF file is exposed as the graph of Values.
//
// Most richer data structures in a PDF file are dictionaries with specific interpretations
// of the name-value pairs. The Font and Page wrappers make the interpretation
// of a specific Value as the corresponding type easier. They are only helpers, though:
// they are implemented only in terms of the Value API and could be moved outside
// the package. Equally important, traversal of other PDF data structures can be implemented
// in other packages as needed.
package xtract

// BUG(rsc): The package is incomplete, although it has been used successfully on some
// large real-world PDF files.

// BUG(rsc): There is no support for closing open PDF files. If you drop all references to a Reader,
// the underlying reader will eventually be garbage collected.

// BUG(rsc): The library makes no attempt at efficiency. A value cache maintained in the Reader
// would probably help significantly.

// BUG(rsc): The support for reading encrypted files is weak.

// BUG(rsc): The Value API does not support error reporting. The intent is to allow users to
// set an error reporting callback in Reader, but that code has not been implemented.

import (
	"bytes"
	"compress/zlib"
	"encoding/ascii85"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/sassoftware/viya-pdf-xtract/logger"
)

// DebugOn is responsible for logging messages into stdout. If problems arise during reading, set it true.
var DebugOn = false

// A Reader is a single PDF file open for reading.
type Reader struct {
	f          io.ReaderAt
	end        int64
	xref       []xref
	trailer    dict
	trailerptr objptr
	key        []byte
	useAES     bool
}

type xref struct {
	ptr      objptr
	inStream bool
	stream   objptr
	offset   int64
}

func Open(file string) (*os.File, *Reader, error) {
	logger.Debug("Open file", true)
	f, err := os.Open(file)
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	logger.Debug(fmt.Sprintf("document: file:%s -- opened (size=%d)", file, fi.Size()), true)
	reader, err := NewReader(f, fi.Size())
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, reader, err
}

// NewReader opens a file for reading, using the data in f with the given total size.
func NewReader(f io.ReaderAt, size int64) (*Reader, error) {
	logger.Debug("Checking Header", true)
	if err := CheckHeader(f); err != nil {
		return nil, err
	}

	logger.Debug("Checking End of file Marker", true)
	if err := ValidateEOFMarker(f, size); err != nil {
		return nil, err
	}

	logger.Debug("Checking Startxref", true)
	startxref, err := FindStartXref(f, size)
	if err != nil {
		return nil, err
	}

	logger.Debug("Checking xref table + trailer", true)

	r := &Reader{f: f, end: size}
	b := newBuffer(io.NewSectionReader(r.f, startxref, r.end-startxref), startxref)
	xref, trailerptr, trailer, err := readXref(r, b)
	if err != nil {
		return nil, err
	}
	r.xref = xref
	r.trailer = trailer
	r.trailerptr = trailerptr

	return r, nil
}

// CheckHeader validates the PDF header at the beginning of the file.
// It ensures the file starts with "%PDF-x.y" and the version is within 1.0–1.7 or 2.0.
func CheckHeader(f io.ReaderAt) error {
	buf := make([]byte, 10)
	n, err := f.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		logger.Error("Failed to read initial bytes for header check: %v", err)
		return err
	}
	if n == 0 {
		logger.Error("not a PDF file: empty")
		return errors.New("not a PDF file: empty")
	}
	buf = buf[:n]
	// Find "%PDF-" possibly not at offset 0 (BOM or garbage before)
	p := bytes.Index(buf, []byte("%PDF-"))
	if p < 0 {
		logger.Error("%PDF possibly not at offset 0 (BOM or garbage before)")
		return err
	}

	// Slice from the header token forward
	lineBuf := buf[p:]

	// Take the first line (up to CR or LF). If no EOL yet, use what we have.
	lineEnd := bytes.IndexAny(lineBuf, "\r\n")
	if lineEnd < 0 {
		lineEnd = len(lineBuf)
	}
	line := lineBuf[:lineEnd]

	// Some files have trailing spaces/tabs/NULLs before the newline; trim them.
	line = bytes.TrimRight(line, " \t\x00")

	// Parse %PDF-x.y (major.minor)
	if !bytes.HasPrefix(line, []byte("%PDF-")) {
		logger.Error("not a PDF file: invalid header (missing %%PDF-)")
		return err
	}
	var major, minor int
	if _, err := fmt.Sscanf(string(line), "%%PDF-%d.%d", &major, &minor); err != nil {
		logger.Error("not a PDF file: malformed version")
		return err
	}

	// Allow 1.0–1.7 and 2.0
	if !((major == 1 && minor >= 0 && minor <= 7) || (major == 2 && minor == 0)) {
		logger.Error(fmt.Sprintf("unsupported PDF version %d.%d", major, minor))
		return err
	}
	// after successful parsing and version validation:
	logger.Debug(fmt.Sprintf("header: PDF-%d.%d", major, minor), true)
	return nil
}

// ValidateEOFMarker checks the last chunk of the file for the "%%EOF" marker.
// Ensures the PDF file is properly terminated as per the specification.
func ValidateEOFMarker(f io.ReaderAt, size int64) error {
	logger.Debug("checking for EOF")
	end := size
	const endChunk = 100
	buf := make([]byte, endChunk)
	f.ReadAt(buf, end-endChunk)
	for len(buf) > 0 && buf[len(buf)-1] == '\n' || buf[len(buf)-1] == '\r' {
		buf = buf[:len(buf)-1]
	}
	buf = bytes.TrimRight(buf, "\r\n\t ")
	if !bytes.HasSuffix(buf, []byte("%%EOF")) {
		logger.Error("not a PDF file: missing %%%%EOF")
		return errors.New(" ")
	}
	return nil
}

// FindStartXref locates and parses the "startxref" pointer near the end of the file.
// Returns the byte offset where the cross-reference table/stream begins.
func FindStartXref(f io.ReaderAt, size int64) (int64, error) {
	const endChunk = 100
	buf := make([]byte, endChunk)
	if _, err := f.ReadAt(buf, size-endChunk); err != nil && err != io.EOF {
		return 0, err
	}
	i := findLastLine(buf, "startxref")
	if i < 0 {
		logger.Error("malformed PDF file: missing final startxref ")
		return 0, errors.New(" ")
	}
	pos := size - endChunk + int64(i)
	b := newBuffer(io.NewSectionReader(f, pos, size-pos), pos)

	tok := b.readToken()
	if tok != keyword("startxref") {
		logger.Error(fmt.Sprintf("malformed PDF file: missing startxref : %v", tok))
		return 0, errors.New(" ")
	}
	startxref, ok := b.readToken().(int64)
	if !ok {
		logger.Error("malformed PDF file: startxref not followed by integer, found: %d", startxref)
		return 0, errors.New(" ")
	}
	logger.Debug(fmt.Sprintf("xref: FindStartXref -- startxref=%d", startxref), true)
	return startxref, nil
}

// Trailer returns the file's Trailer value.
func (r *Reader) Trailer() Value {
	return Value{r, r.trailerptr, r.trailer}
}

func readXref(r *Reader, b *buffer) ([]xref, objptr, dict, error) {
	tok := b.readToken()
	if tok == keyword("xref") {
		logger.Debug("Found Xref Table", true)
		return readXrefTable(r, b)
	}
	if _, ok := tok.(int64); ok {
		b.unreadToken(tok)
		logger.Debug("Found Xref Stream", true)
		return readXrefStream(r, b)
	}
	logger.Error(fmt.Sprintf("malformed PDF: cross-reference table nor stream found: %v", tok))
	return nil, objptr{}, nil, errors.New(" ")
}

func readXrefStream(r *Reader, b *buffer) ([]xref, objptr, dict, error) {
	logger.Debug("processing Xref Stream")
	strmptr, strm, err := parseXrefStreamObject(b)
	if err != nil {
		return nil, objptr{}, nil, err
	}
	//Extract /Size and allocate the table.
	size, err := xrefSize(strm)
	if err != nil {
		return nil, objptr{}, nil, err
	}
	table := make([]xref, size)
	//Fill entries from the first stream.
	table, err = readXrefStreamData(r, strm, table, size)
	if err != nil {
		return nil, objptr{}, nil, fmt.Errorf("malformed PDF: %v", err)
	}
	// Follow and merge any /Prev streams.
	table, err = mergePrevXrefStreams(r, strm, table, size)
	if err != nil {
		return nil, objptr{}, nil, err
	}

	return table, strmptr, strm.hdr, nil
}

// reads one object from buffer and returns its objptr and stream,
// ensuring it's an /XRef stream.
func parseXrefStreamObject(b *buffer) (objptr, stream, error) {
	logger.Debug("reading xref stream at offset %v", b.pos)
	obj1 := b.readObject()
	od, ok := obj1.(objdef)
	if !ok {
		logger.Error(fmt.Sprintf("malformed PDF: objdef not found: %v", objfmt(obj1)))
		return objptr{}, stream{}, errors.New(" ")
	}
	strm, ok := od.obj.(stream)
	if !ok {
		logger.Error(fmt.Sprintf("malformed PDF: cross-reference stream not found: %v", objfmt(od)))
		return objptr{}, stream{}, errors.New(" ")
	}
	if strm.hdr["Type"] != name("XRef") {
		logger.Error("malformed PDF: xref stream does not have type XRef")
		return objptr{}, stream{}, errors.New(" ")
	}

	return od.ptr, strm, nil
}

// xrefSize returns the /Size from an xref stream header.
func xrefSize(strm stream) (int64, error) {
	if size, ok := strm.hdr["Size"].(int64); ok {
		logger.Debug("xref stream size: %d", size)
		return size, nil
	}
	logger.Error("malformed PDF: xref stream missing Size")
	return 0, errors.New(" ")
}

// Navigates and goes to /Prev chain, validating and merging each older stream.
func mergePrevXrefStreams(r *Reader, cur stream, table []xref, maxSize int64) ([]xref, error) {
	for prevoff := cur.hdr["Prev"]; prevoff != nil; {
		off, ok := prevoff.(int64)
		logger.Debug(fmt.Sprintf("found Prev stream wiht offset %d", off), true)
		if !ok {
			logger.Error(fmt.Sprintf("malformed PDF: xref Prev is not integer: %v", prevoff))
			return nil, errors.New(" ")
		}
		// Open a buffer at the previous xref stream offset and parse it.
		b := newBuffer(io.NewSectionReader(r.f, off, r.end-off), off)
		_, prevStrm, err := parseXrefStreamObject(b)
		if err != nil {
			return nil, err
		}
		prevoff = prevStrm.hdr["Prev"]
		prevVal := Value{r, objptr{}, prevStrm}
		if prevVal.Kind() != Stream {
			logger.Error(fmt.Sprintf("malformed PDF: xref prev stream is not stream: %v", prevVal))
			return nil, errors.New(" ")
		}
		if prevVal.Key("Type").Name() != "XRef" {
			logger.Error("malformed PDF: xref prev stream does not have type XRef")
			return nil, errors.New(" ")
		}
		// Size checks and merge.
		psize := prevVal.Key("Size").Int64()
		if psize > maxSize {
			logger.Error("malformed PDF: xref prev stream larger than last stream")
			return nil, errors.New(" ")
		}
		table, err = readXrefStreamData(r, prevVal.data.(stream), table, psize)
		if err != nil {
			logger.Error(fmt.Sprintf("malformed PDF: reading xref prev stream: %v", err))
			return nil, errors.New(" ")
		}
	}
	logger.Debug("merged Prev stream data")
	return table, nil
}

func readXrefStreamData(r *Reader, strm stream, table []xref, size int64) ([]xref, error) {
	// gather filter names (safely)
	var filters []string
	if f := strm.hdr["Filter"]; f != nil {
		switch fv := f.(type) {
		case name:
			filters = append(filters, string(fv))
		case array:
			for i := 0; i < len(fv); i++ {
				if nm, ok := fv[i].(name); ok {
					filters = append(filters, string(nm))
				}
			}
		}
	}
	declLen := int64(0)
	if L, ok := strm.hdr["Length"].(int64); ok {
		declLen = L
	}
	logger.Debug(fmt.Sprintf("stream: obj %d %d (declLen=%d) filters=%v",
		strm.ptr.id, strm.ptr.gen, declLen, filters), true)

	index, _ := strm.hdr["Index"].(array)
	if index == nil {
		index = array{int64(0), size}
	}
	if len(index)%2 != 0 {
		err := fmt.Errorf("invalid Index array %v", objfmt(index))
		logger.Error(err.Error())
		return nil, err
	}

	ww, ok := strm.hdr["W"].(array)
	if !ok {
		err := fmt.Errorf("xref stream missing W array")
		logger.Error(err.Error())
		return nil, err
	}

	var w []int
	for _, x := range ww {
		i, ok := x.(int64)
		if !ok || int64(int(i)) != i {
			err := fmt.Errorf("invalid W array %v", objfmt(ww))
			logger.Error(err.Error())
			return nil, err
		}
		w = append(w, int(i))
	}
	if len(w) < 3 {
		err := fmt.Errorf("invalid W array %v", objfmt(ww))
		logger.Error(err.Error())
		return nil, err
	}

	v := Value{r, objptr{}, strm}
	wtotal := 0
	for _, wid := range w {
		wtotal += wid
	}
	buf := make([]byte, wtotal)
	data := v.Reader()
	for len(index) > 0 {
		start, ok1 := index[0].(int64)
		n, ok2 := index[1].(int64)
		if !ok1 || !ok2 {
			err := fmt.Errorf("malformed Index pair %v %v %T %T", objfmt(index[0]), objfmt(index[1]), index[0], index[1])
			logger.Error(err.Error())
			return nil, err
		}
		index = index[2:]
		for i := 0; i < int(n); i++ {
			_, err := io.ReadFull(data, buf)
			if err != nil {
				err = fmt.Errorf("error reading xref stream: %v", err)
				logger.Error(err.Error())
				return nil, err
			}
			v1 := decodeInt(buf[0:w[0]])
			if w[0] == 0 {
				v1 = 1
			}
			v2 := decodeInt(buf[w[0] : w[0]+w[1]])
			v3 := decodeInt(buf[w[0]+w[1] : w[0]+w[1]+w[2]])
			x := int(start) + i
			for cap(table) <= x {
				table = append(table[:cap(table)], xref{})
			}
			if table[x].ptr != (objptr{}) {
				continue
			}
			switch v1 {
			case 0:
				table[x] = xref{ptr: objptr{0, 65535}}
			case 1:
				table[x] = xref{ptr: objptr{uint32(x), uint16(v3)}, offset: int64(v2)}
			case 2:
				table[x] = xref{ptr: objptr{uint32(x), 0}, inStream: true, stream: objptr{uint32(v2), 0}, offset: int64(v3)}
			default:
				if DebugOn {
					logger.Error(fmt.Sprintf("invalid xref stream type %d: %x", v1, buf))
				}
			}
		}
	}
	logger.Debug(fmt.Sprintf("parseXrefEntries (entries parsed=%d)", size), true)

	return table, nil
}

func decodeInt(b []byte) int {
	x := 0
	for _, c := range b {
		x = x<<8 | int(c)
	}
	return x
}

func readXrefTable(r *Reader, b *buffer) ([]xref, objptr, dict, error) {
	logger.Debug("processing xref table")
	table, trailer, err := parseXrefTableAndTrailer(b, nil)
	if err != nil {
		return nil, objptr{}, nil, err
	}

	// This will parse the xref stream pointed to by the trailer and merge its entries.
	table, trailer, err = r.handleTrailerXRefStm(table, trailer)
	if err != nil {
		logger.Error("readXrefTable: XRefStm handling error: %v. Falling back to Prev chain.", err)
		// proceed with Prev chain to salvage what we can from ASCII tables.
	}

	// Follow the Prev chain if present
	table, trailer, err = resolvePrevXrefTables(r, trailer, table)
	if err != nil {
		return nil, objptr{}, nil, err
	}

	// Validate and finalize
	if err := validateTrailerSize(&table, trailer); err != nil {
		return nil, objptr{}, nil, err
	}

	return table, objptr{}, trailer, nil

}

// parseXrefTableAndTrailer parses a single xref table section
// and the trailer dictionary that follows it.
func parseXrefTableAndTrailer(b *buffer, table []xref) ([]xref, dict, error) {
	var err error
	table, err = readXrefTableData(b, table)
	if err != nil {
		logger.Error(fmt.Sprintf("malformed PDF: %v", err))
		return nil, nil, errors.New(" ")
	}
	logger.Debug("Parsed xref table section with %d entries so far\n", len(table))
	trailer, ok := b.readObject().(dict)
	if !ok {
		logger.Error("malformed PDF: xref table not followed by trailer dictionary")
		return nil, nil, errors.New(" ")
	}
	return table, trailer, nil
}

func resolvePrevXrefTables(r *Reader, trailer dict, table []xref) ([]xref, dict, error) {
	for prevoff := trailer[name("Prev")]; prevoff != nil; {
		off, ok := prevoff.(int64)
		logger.Debug("found Prev xref table", true)
		if !ok {
			logger.Error(fmt.Sprintf("malformed PDF: xref Prev is not integer: %v", prevoff))
			return nil, nil, errors.New(" ")
		}
		b := newBuffer(io.NewSectionReader(r.f, off, r.end-off), off)
		// Prev must start with "xref"
		tok := b.readToken()
		if tok != keyword("xref") {
			logger.Error("malformed PDF: xref Prev does not point to xref")
			return nil, nil, errors.New(" ")
		}
		var err error
		table, trailer, err = parseXrefTableAndTrailer(b, table)
		if err != nil {
			logger.Error(fmt.Sprintf("malformed PDF: %v", err))
			return nil, nil, errors.New(" ")
		}
		// call handleTrailerXRefStm for this older trailer before walking further Prev
		table, trailer, err = r.handleTrailerXRefStm(table, trailer)
		if err != nil {
			logger.Debug("warning: XRefStm handling error in Prev chain: %v; continuing\n", err)
			// continue even if XRefStm handling failed for this prev trailer
		}
		prevoff = trailer[name("Prev")]
	}
	return table, trailer, nil
}

// validateTrailerSize trims the xref table to the declared /Size in trailer.
func validateTrailerSize(table *[]xref, trailer dict) error {
	size, ok := trailer[name("Size")].(int64)
	if !ok {
		logger.Error("malformed PDF: trailer missing /Size entry")
		return errors.New(" ")
	}

	if size < int64(len(*table)) {
		*table = (*table)[:size]
	}
	logger.Debug("trailer size validated: %d", size)
	return nil
}

// ensureLen makes sure s has length at least n (growing capacity if needed)
// and returns the possibly-reallocated slice.
func ensureLen[T any](s []T, n int) []T {
	if n <= len(s) {
		return s
	}
	if cap(s) < n {
		ns := make([]T, n)
		copy(ns, s)
		return ns
	}
	return s[:n]
}

// setIfEmpty sets table[x] to val only if the slot is currently empty.
func setIfEmpty(table *[]xref, x int, val xref) {
	if x < 0 {
		return
	}
	*table = ensureLen(*table, x+1)
	if (*table)[x].ptr == (objptr{}) {
		(*table)[x] = val
	}
}

func readXrefTableData(b *buffer, table []xref) ([]xref, error) {
	logger.Debug("reading xref table data")
	for {
		tok := b.readToken()
		if tok == keyword("trailer") {
			break
		}
		start, ok1 := tok.(int64)
		count, ok2 := b.readToken().(int64)
		if !ok1 || !ok2 || start < 0 || count < 0 {
			logger.Error("malformed xref table subsection header")
			return nil, errors.New(" ")
		}
		for i := 0; i < int(count); i++ {
			offTok := b.readToken()
			genTok := b.readToken()
			allocTok := b.readToken()

			off, okOff := offTok.(int64)
			gen, okGen := genTok.(int64)
			alloc, okAlloc := allocTok.(keyword)
			if !okOff || !okGen || !okAlloc {
				logger.Error(fmt.Sprintf("malformed xref entry at subsection starting %d", start))
				return nil, errors.New(" ")
			}

			idx := int(start) + i
			switch alloc {
			case keyword("n"): // in-use — record if empty
				setIfEmpty(&table, idx, xref{ptr: objptr{uint32(idx), uint16(gen)}, offset: off})
			case keyword("f"): // free — ensure slice long enough for safe indexing
				table = ensureLen(table, idx+1)
			default:
				logger.Error(fmt.Sprintf("malformed xref table: unexpected alloc token %v", alloc))
				return nil, errors.New(" ")
			}
		}
	}
	return table, nil
}

// mergeXrefTables merges src into dest using conservative rules:
// - extend dest if src bigger
// - if dest empty => accept src
// - if dest free (gen==65535) and src in-use => replace
// - if both in-use => prefer src (stream authoritative)
func mergeXrefTables(dest []xref, src []xref) []xref {
	if len(src) > len(dest) {
		nd := make([]xref, len(src))
		copy(nd, dest)
		dest = nd
	}
	for i := 0; i < len(src); i++ {
		s := src[i]
		if s.ptr == (objptr{}) {
			continue
		}
		d := dest[i]
		if d.ptr == (objptr{}) {
			dest[i] = s
			continue
		}
		// both in-use: prefer src (xref-stream authoritative)
		if d.ptr.gen != 65535 && s.ptr.gen != 65535 {
			dest[i] = s
			continue
		}
		// otherwise keep dest
	}
	return dest
}

// isLikelyObjectAt performs a lightweight check whether an object header or dict begins at off.
func (r *Reader) isLikelyObjectAt(off int64) bool {
	if off < 0 || off >= r.end {
		return false
	}
	buf := make([]byte, 64)
	n, err := r.f.ReadAt(buf, off)
	if err != nil && err != io.EOF {
		return false
	}
	if n == 0 {
		return false
	}
	s := string(buf[:n])
	sTrim := strings.TrimLeft(s, " \t\r\n")
	// match "N G obj" or starting dict "<<" or PDF header
	re := regexp.MustCompile(`^\d+\s+\d+\s+obj\b`)
	if re.MatchString(sTrim) {
		return true
	}
	if strings.HasPrefix(sTrim, "<<") {
		return true
	}
	if strings.HasPrefix(sTrim, "%PDF-") {
		return true
	}
	return false
}

// scanForObjectAt searches a +-window around approx for "<id> <gen> obj" and returns found offset or -1.
func (r *Reader) scanForObjectAt(id uint32, gen uint16, approx int64, window int64) int64 {
	if approx < 0 {
		approx = 0
	}
	start := approx - window
	if start < 0 {
		start = 0
	}
	end := approx + window
	if end > r.end {
		end = r.end
	}
	size := end - start
	if size <= 0 {
		return -1
	}
	buf := make([]byte, size)
	n, err := r.f.ReadAt(buf, start)
	if err != nil && err != io.EOF {
		return -1
	}
	buf = buf[:n]
	pattern := fmt.Sprintf(`\b%d\s+%d\s+obj\b`, id, gen)
	re := regexp.MustCompile(pattern)
	loc := re.FindIndex(buf)
	if loc == nil {
		return -1
	}
	return start + int64(loc[0])
}

// validateAndRepairXrefEntries checks offsets in table and tries to repair with a small-window scan.
// Returns counts: repaired entries and invalid (unrepairable) entries.
func (r *Reader) validateAndRepairXrefEntries(table []xref) (repaired int, invalid int) {
	repaired = 0
	invalid = 0
	for i := 0; i < len(table); i++ {
		ent := table[i]
		if ent.ptr == (objptr{}) {
			continue
		}
		if ent.offset == 0 {
			// no external file offset to validate (in-stream or free)
			continue
		}
		if r.isLikelyObjectAt(ent.offset) {
			continue
		}
		// attempt small-window scan ±1024
		found := r.scanForObjectAt(ent.ptr.id, ent.ptr.gen, ent.offset, 1024)
		if found >= 0 {
			table[i].offset = found
			repaired++
			continue
		}
		invalid++
	}
	return
}

// handleTrailerXRefStm: if trailer contains /XRefStm, parse that stream and merge its table into the provided table.
// Also recursively merges any /Prev chains for streams. If the stream appears too invalid, returns error so caller can fallback.
func (r *Reader) handleTrailerXRefStm(table []xref, trailer dict) ([]xref, dict, error) {
	xrefstm := trailer[name("XRefStm")]
	if xrefstm == nil {
		return table, trailer, nil
	}
	logger.Debug("found XRefStm in trailer", true)
	off, ok := xrefstm.(int64)
	if !ok {
		logger.Error(fmt.Sprintf("malformed PDF: XRefStm not integer: %v", xrefstm))
		return table, trailer, errors.New(" ")
	}
	b := newBuffer(io.NewSectionReader(r.f, off, r.end-off), off)
	srcTable, _, hdr, err := readXrefStream(r, b)
	if err != nil {
		logger.Error(fmt.Sprintf("failed to parse XRefStm at %d: %v", off, err))
		return table, trailer, errors.New(" ")
	}
	// validate & attempt repair on srcTable offsets
	repaired, invalid := r.validateAndRepairXrefEntries(srcTable)
	_ = repaired

	total := 0
	for _, e := range srcTable {
		if e.ptr != (objptr{}) {
			total++
		}
	}
	// Accept or reject the stream table based on an invalid threshold
	if total > 0 && float64(invalid)/float64(total) > 0.30 {
		logger.Error(fmt.Sprintf("xref stream at %d appears invalid: %d/%d invalid entries", off, invalid, total))
		return table, trailer, errors.New(" ")
	}

	// Merge the stream table into the main ASCII table.
	table = mergeXrefTables(table, srcTable)

	if _, ok := hdr["Size"]; !ok {
		logger.Debug(fmt.Sprintf("xref stream at %d missing /Size", off))
		return table, trailer, errors.New(" ")
	}
	return table, trailer, nil
}

// findLastLine searches backwards in buf for the last occurrence of the
// keyword s (e.g. "startxref") that is correctly terminated.
//
// In the PDF specification (ISO 32000), the keyword "startxref" must be
// followed by an end-of-line (EOL) marker, then an integer offset, then
// another EOL and finally %%EOF. However, many real-world PDFs are not
// strictly spec-compliant. Producers often insert trailing spaces, tabs,
// nulls, or other whitespace characters after "startxref" before the
// required newline.
//
// Example of valid cases (all should be accepted):
//
//	startxref\n
//	1234
//	%%EOF
//
//	startxref\r\n
//	5678
//	%%EOF
//
//	startxref␠\n          <-- extra space before newline
//	9012
//	%%EOF
//
//	startxref␠␠\t\r\n     <-- multiple spaces + tab before newline
//	3456
//	%%EOF
//
//	startxref\0\0\n       <-- includes NULLs before newline
//	7890
//	%%EOF
//
// The original implementation only accepted the strict case where the
// keyword was immediately followed by '\n' or '\r'. This caused failures
// when parsing non-conforming but commonly encountered PDFs.
//
// Changes made:
//   - Added isPDFWhite() helper that recognizes all PDF-defined whitespace
//     characters: 0x00 (null), 0x09 (tab), 0x0A (LF), 0x0C (FF),
//     0x0D (CR), and 0x20 (space).
//   - After finding "startxref", we now skip over all such whitespace.
//   - We then check that at least one of the skipped characters was a
//     valid EOL (CR or LF). This ensures we accept both strict and
//     relaxed forms while still requiring a proper line ending.
//
// This makes the parser robust against malformed but widely accepted PDFs
// while remaining compliant with the official grammar for well-formed files.
func findLastLine(buf []byte, s string) int {
	bs := []byte(s)
	var indices []int

	// Collect all occurrences in a single pass
	for i := 0; ; {
		j := bytes.Index(buf[i:], bs)
		if j < 0 {
			break
		}
		indices = append(indices, i+j)
		i += j + 1 // move forward
	}

	// Walk backwards through matches
	for k := len(indices) - 1; k >= 0; k-- {
		i := indices[k]
		j := SkipWhitespace(buf, i+len(bs))
		if EndsWithEOL(buf, i+len(bs), j) {
			return i
		}
	}
	return -1
}

var wsBits [4]uint64 // 256 bits = 4 * 64

func init() {
	for _, b := range []byte{0x00, 0x09, 0x0A, 0x0C, 0x0D, 0x20} {
		wsBits[b>>6] |= 1 << (b & 63)
	}
}

// isWhitespace reports whether b is one of the six whitespace characters
// defined by ISO 32000-1 §7.2.2 for PDF syntax: 00, 09, 0A, 0C, 0D, 20.
// Note: This is PDF-specific whitespace, not Unicode or Go's definition.
func isWhitespace(b byte) bool {
	logger.Debug("white space found")
	return (wsBits[b>>6] & (1 << (b & 63))) != 0
}

// SkipWhitespace advances j past all whitespace.
func SkipWhitespace(buf []byte, j int) int {
	logger.Debug("skipping whitespace")
	for j < len(buf) && isWhitespace(buf[j]) {
		j++
	}
	return j
}

// EndsWithEOL checks if the last skipped char is CR or LF.
func EndsWithEOL(buf []byte, start, end int) bool {
	if end > start {
		last := buf[end-1]
		return last == '\n' || last == '\r'
	}
	return false
}

// A Value is a single PDF value, such as an integer, dictionary, or array.
// The zero Value is a PDF null (Kind() == Null, IsNull() = true).
type Value struct {
	r    *Reader
	ptr  objptr
	data interface{}
}

// IsNull reports whether the value is a null. It is equivalent to Kind() == Null.
func (v Value) IsNull() bool {
	return v.data == nil
}

// A ValueKind specifies the kind of data underlying a Value.
type ValueKind int

// The PDF value kinds.
const (
	Null ValueKind = iota
	Bool
	Integer
	Real
	String
	Name
	Dict
	Array
	Stream
)

// Kind reports the kind of value underlying v.
func (v Value) Kind() ValueKind {

	switch v.data.(type) {
	default:
		return Null
	case bool:
		return Bool
	case int64:
		return Integer
	case float64:
		return Real
	case string:
		return String
	case name:
		return Name
	case dict:
		return Dict
	case array:
		return Array
	case stream:
		return Stream
	}
}

// String returns a textual representation of the value v.
// Note that String is not the accessor for values with Kind() == String.
// To access such values, see RawString, Text, and TextFromUTF16.
func (v Value) String() string {
	return objfmt(v.data)
}

func objfmt(x interface{}) string {
	switch x := x.(type) {
	default:
		return fmt.Sprint(x)
	case string:
		if isPDFDocEncoded(x) {
			return strconv.Quote(pdfDocDecode(x))
		}
		if isUTF16(x) {
			return strconv.Quote(utf16Decode(x[2:]))
		}
		return strconv.Quote(x)
	case name:
		return "/" + string(x)
	case dict:
		var keys []string
		for k := range x {
			keys = append(keys, string(k))
		}
		sort.Strings(keys)
		var buf bytes.Buffer
		buf.WriteString("<<")
		for i, k := range keys {
			elem := x[name(k)]
			if i > 0 {
				buf.WriteString(" ")
			}
			buf.WriteString("/")
			buf.WriteString(k)
			buf.WriteString(" ")
			buf.WriteString(objfmt(elem))
		}
		buf.WriteString(">>")
		return buf.String()

	case array:
		var buf bytes.Buffer
		buf.WriteString("[")
		for i, elem := range x {
			if i > 0 {
				buf.WriteString(" ")
			}
			buf.WriteString(objfmt(elem))
		}
		buf.WriteString("]")
		return buf.String()

	case stream:
		return fmt.Sprintf("%v@%d", objfmt(x.hdr), x.offset)

	case objptr:
		return fmt.Sprintf("%d %d R", x.id, x.gen)

	case objdef:
		return fmt.Sprintf("{%d %d obj}%v", x.ptr.id, x.ptr.gen, objfmt(x.obj))
	}
}

// Bool returns v's boolean value.
// If v.Kind() != Bool, Bool returns false.
func (v Value) Bool() bool {
	x, ok := v.data.(bool)
	if !ok {
		return false
	}
	return x
}

// Int64 returns v's int64 value.
// If v.Kind() != Int64, Int64 returns 0.
func (v Value) Int64() int64 {
	x, ok := v.data.(int64)
	if !ok {
		return 0
	}
	return x
}

// Float64 returns v's float64 value, converting from integer if necessary.
// If v.Kind() != Float64 and v.Kind() != Int64, Float64 returns 0.
func (v Value) Float64() float64 {
	x, ok := v.data.(float64)
	if !ok {
		x, ok := v.data.(int64)
		if ok {
			return float64(x)
		}
		return 0
	}
	return x
}

// RawString returns v's string value.
// If v.Kind() != String, RawString returns the empty string.
func (v Value) RawString() string {
	x, ok := v.data.(string)
	if !ok {
		return ""
	}
	return x
}

// Text returns v's string value interpreted as a “text string” (defined in the PDF spec)
// and converted to UTF-8.
// If v.Kind() != String, Text returns the empty string.
func (v Value) Text() string {
	x, ok := v.data.(string)
	if !ok {
		return ""
	}
	if isPDFDocEncoded(x) {
		return pdfDocDecode(x)
	}
	if isUTF16(x) {
		return utf16Decode(x[2:])
	}
	return x
}

// TextFromUTF16 returns v's string value interpreted as big-endian UTF-16
// and then converted to UTF-8.
// If v.Kind() != String or if the data is not valid UTF-16, TextFromUTF16 returns
// the empty string.
func (v Value) TextFromUTF16() string {
	x, ok := v.data.(string)
	if !ok {
		return ""
	}
	if len(x)%2 == 1 {
		return ""
	}
	if x == "" {
		return ""
	}
	return utf16Decode(x)
}

// Name returns v's name value.
// If v.Kind() != Name, Name returns the empty string.
// The returned name does not include the leading slash:
// if v corresponds to the name written using the syntax /Helvetica,
// Name() == "Helvetica".
func (v Value) Name() string {
	x, ok := v.data.(name)
	if !ok {
		return ""
	}
	return string(x)
}

// Key returns the value associated with the given name key in the dictionary v.
// Like the result of the Name method, the key should not include a leading slash.
// If v is a stream, Key applies to the stream's header dictionary.
// If v.Kind() != Dict and v.Kind() != Stream, Key returns a null Value.
func (v Value) Key(key string) Value {
	x, ok := v.data.(dict)
	if !ok {
		strm, ok := v.data.(stream)
		if !ok {
			return Value{}
		}
		x = strm.hdr
	}
	return v.r.resolve(v.ptr, x[name(key)])
}

// Keys returns a sorted list of the keys in the dictionary v.
// If v is a stream, Keys applies to the stream's header dictionary.
// If v.Kind() != Dict and v.Kind() != Stream, Keys returns nil.
func (v Value) Keys() []string {
	x, ok := v.data.(dict)
	if !ok {
		strm, ok := v.data.(stream)
		if !ok {
			return nil
		}
		x = strm.hdr
	}
	keys := []string{} // not nil
	for k := range x {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	return keys
}

// Index returns the i'th element in the array v.
// If v.Kind() != Array or if i is outside the array bounds,
// Index returns a null Value.
func (v Value) Index(i int) Value {
	x, ok := v.data.(array)
	if !ok || i < 0 || i >= len(x) {
		return Value{}
	}
	return v.r.resolve(v.ptr, x[i])
}

// Len returns the length of the array v.
// If v.Kind() != Array, Len returns 0.
func (v Value) Len() int {
	x, ok := v.data.(array)
	if !ok {
		return 0
	}
	return len(x)
}

func (r *Reader) resolve(parent objptr, x interface{}) Value {
	//logger.Debug("resolving objects")
	if ptr, ok := x.(objptr); ok {
		if ptr.id >= uint32(len(r.xref)) {
			return Value{}
		}
		xref := r.xref[ptr.id]
		if xref.ptr != ptr || !xref.inStream && xref.offset == 0 {
			return Value{}
		}
		var obj object
		if xref.inStream {
			strm := r.resolve(parent, xref.stream)
		Search:
			for {
				if strm.Kind() != Stream {
					logger.Error("not a stream")
					panic("not a stream")
				}
				if strm.Key("Type").Name() != "ObjStm" {
					logger.Error("not an object stream")
					panic("not an object stream")
				}
				n := int(strm.Key("N").Int64())
				first := strm.Key("First").Int64()
				if first == 0 {
					logger.Error("missing First")
					panic("missing First")
				}
				b := newBuffer(strm.Reader(), 0)
				b.allowEOF = true
				for i := 0; i < n; i++ {
					id, _ := b.readToken().(int64)
					off, _ := b.readToken().(int64)
					if uint32(id) == ptr.id {
						b.seekForward(first + off)
						logger.Debug("readobj1")
						x = b.readObject()
						break Search
					}
				}
				ext := strm.Key("Extends")
				if ext.Kind() != Stream {
					logger.Error("cannot find object in stream")
					panic("cannot find object in stream")
				}
				strm = ext
			}
		} else {
			b := newBuffer(io.NewSectionReader(r.f, xref.offset, r.end-xref.offset), xref.offset)
			b.key = r.key
			b.useAES = r.useAES
			obj = b.readObject()
			def, ok := obj.(objdef)
			if !ok {
				logger.Error(fmt.Sprintf("loading %v: found %T instead of objdef", ptr, obj))
				panic(fmt.Errorf("loading %v: found %T instead of objdef", ptr, obj))
				//return Value{}
			}
			if def.ptr != ptr {
				logger.Error(fmt.Sprintf("loading %v: found %v", ptr, def.ptr))
				panic(fmt.Errorf("loading %v: found %v", ptr, def.ptr))
			}
			x = def.obj
			if d, ok := x.(dict); ok {
				typ := name("")
				if t, ok := d[name("Type")].(name); ok {
					typ = t
				}
				if typ == name("Pages") {
					// Count
					count := int64(0)
					if c, ok := d[name("Count")].(int64); ok {
						count = c
					}
					// Kids formatting
					kidsStr := ""
					if kidsObj, ok := d[name("Kids")].(array); ok {
						var parts []string
						for _, kid := range kidsObj {
							if ptr, ok := kid.(objptr); ok {
								parts = append(parts, fmt.Sprintf("%d %d R", ptr.id, ptr.gen))
							} else {
								parts = append(parts, objfmt(kid))
							}
						}
						kidsStr = fmt.Sprintf("[%s]", strings.Join(parts, " "))
					}
					logger.Debug(fmt.Sprintf("object: %d %d (Pages) -- Count=%d, Kids=%s",
						def.ptr.id, def.ptr.gen, count, kidsStr), true)
				}
				if typ == name("Page") {
					// Resources & Contents
					res := d[name("Resources")]
					contents := d[name("Contents")]
					formatObj := func(o interface{}) string {
						if p, ok := o.(objptr); ok {
							return fmt.Sprintf("%d %d R", p.id, p.gen)
						}
						return objfmt(o)
					}
					logger.Debug(fmt.Sprintf("page: obj %d %d -- /Resources %s /Contents %s",
						def.ptr.id, def.ptr.gen, formatObj(res), formatObj(contents)), true)
				}
			}
		}
		parent = ptr
	}

	switch x := x.(type) {
	case nil, bool, int64, float64, name, dict, array, stream:
		return Value{r, parent, x}
	case string:
		return Value{r, parent, x}
	default:
		logger.Error(fmt.Sprintf("unexpected value type %T in resolve", x))
		panic(fmt.Errorf("unexpected value type %T in resolve", x))
	}
}

type errorReadCloser struct {
	err error
}

func (e *errorReadCloser) Read([]byte) (int, error) {
	return 0, e.err
}

func (e *errorReadCloser) Close() error {
	return e.err
}

// Reader returns the data contained in the stream v.
// If v.Kind() != Stream, Reader returns a ReadCloser that
// responds to all reads with a “stream not present” error.
func (v Value) Reader() io.ReadCloser {
	logger.Debug("Reader: reading the data contained in the stream")

	x, ok := v.data.(stream)
	if !ok {
		logger.Error("stream not present")
		return &errorReadCloser{fmt.Errorf("stream not present")}
	}
	var rd io.Reader
	rd = io.NewSectionReader(v.r.f, x.offset, v.Key("Length").Int64())
	filter := v.Key("Filter")
	param := v.Key("DecodeParms")
	switch filter.Kind() {
	default:
		logger.Error(fmt.Sprintf("unsupported filter %v", filter))
		panic(fmt.Errorf("unsupported filter %v", filter))
	case Null:
		// ok
	case Name:
		rd = applyFilter(rd, filter.Name(), param)
	case Array:
		for i := 0; i < filter.Len(); i++ {
			rd = applyFilter(rd, filter.Index(i).Name(), param.Index(i))
		}
	}

	return ioutil.NopCloser(rd)
}

func applyFilter(rd io.Reader, name string, param Value) io.Reader {
	logger.Debug("applyFilter")
	switch name {
	default:
		logger.Error("unknown filter " + name)
		panic("unknown filter " + name)
	case "FlateDecode":
		zr, err := zlib.NewReader(rd)
		if err != nil {
			logger.Error(err.Error())
			panic(err)
		}
		logger.Debug("filter: FlateDecode (decoder initialized)", true)
		pred := param.Key("Predictor")
		if pred.Kind() == Null {
			return zr
		}
		columns := param.Key("Columns").Int64()
		switch pred.Int64() {
		default:
			logger.Error(fmt.Sprintf("unknown predictor %d", pred.data))
			panic("pred")
		case 12:
			return &pngUpReader{r: zr, hist: make([]byte, 1+columns), tmp: make([]byte, 1+columns)}
		}
	case "ASCII85Decode":
		cleanASCII85 := newAlphaReader(rd)
		decoder := ascii85.NewDecoder(cleanASCII85)

		switch param.Keys() {
		default:
			logger.Error("not expected DecodeParms for ascii85")
			panic("not expected DecodeParms for ascii85")
		case nil:
			return decoder
		}
	}
}

type pngUpReader struct {
	r    io.Reader
	hist []byte
	tmp  []byte
	pend []byte
}

func (r *pngUpReader) Read(b []byte) (int, error) {
	n := 0
	for len(b) > 0 {
		if len(r.pend) > 0 {
			m := copy(b, r.pend)
			n += m
			b = b[m:]
			r.pend = r.pend[m:]
			continue
		}
		_, err := io.ReadFull(r.r, r.tmp)
		if err != nil {
			return n, err
		}
		if r.tmp[0] != 2 {
			logger.Error("malformed PNG-Up encoding")
			return n, errors.New(" ")
		}
		for i, b := range r.tmp {
			r.hist[i] += b
		}
		r.pend = r.hist[1:]
	}
	return n, nil
}

