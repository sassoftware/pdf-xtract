// Copyright © 2026, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: BSD-3-Clause

package xtract

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStripXMLTags(t *testing.T) {
	in := `<p>Hello <b>World</b> &amp; <i>Gophers</i></p>`
	out := stripXMLTags(in)
	assert.Equal(t, "Hello World &amp; Gophers", out)
}

func TestParseXMPWithXML(t *testing.T) {
	ra, size, done := openReaderAt(t, "metadata.pdf")
	defer done()

	r, err := NewReader(ra, size)
	require.NoError(t, err, "NewReaderEncrypted should succeed")

	// Read raw XMP XML from the PDF's /Root/Metadata stream
	xmpXML, err := r.readXMP()
	require.NoError(t, err, "readXMP should not error")
	require.NotEmpty(t, xmpXML, "PDF should contain an XMP metadata stream")

	// Parse XMP and assert fields
	got, ok := parseXMPWithXML(xmpXML)
	require.True(t, ok, "parseXMPWithXML should successfully parse XMP from PDF")

	// Assert expected fields that you know are present in your sample PDF
	assert.Equal(t, "Minimal PDF with Metadata", got.Title)
	assert.Equal(t, "UnitTest PDF Generator", got.Producer)
	// Create/Modify date in your sample appears to be present
	assert.NotEmpty(t, got.CreateDate)
	assert.NotEmpty(t, got.ModifyDate)
}

func TestParseXMPWithXML_Invalid(t *testing.T) {
	// malformed XML should return ok==false
	xmp := `<xmpmeta><not-closed>`
	_, ok := parseXMPWithXML(xmp)
	assert.False(t, ok)
}

func TestParseXMPFallback(t *testing.T) {
	// Prepare a simple XMP-like blob where tags are present but XML may be messy.
	xmp := `
  <dc:title><rdf:li>Fallback Title</rdf:li></dc:title>
  <dc:creator><rdf:li>Fallback Creator</rdf:li></dc:creator>
  <dc:description><rdf:li>Fallback Subject</rdf:li></dc:description>
  <pdf:Keywords>k1,k2</pdf:Keywords>
  <xmp:CreatorTool>FallbackTool</xmp:CreatorTool>
  <pdf:Producer>FallbackProducer</pdf:Producer>
  <xmp:CreateDate>2021-04-05</xmp:CreateDate>
  <xmp:ModifyDate>2021-04-06</xmp:ModifyDate>
`
	got := parseXMPFallback(xmp)
	assert.Equal(t, "Fallback Title", got.Title)
	assert.Equal(t, "Fallback Creator", got.Creator)
	assert.Equal(t, "Fallback Subject", got.Subject)
	assert.Equal(t, "k1,k2", got.Keywords)
	assert.Equal(t, "FallbackTool", got.CreatorTool)
	assert.Equal(t, "FallbackProducer", got.Producer)
	assert.Equal(t, "2021-04-05", got.CreateDate)
	assert.Equal(t, "2021-04-06", got.ModifyDate)
}

func TestHeaderVersion(t *testing.T) {
	blob := []byte("junk\n%PDF-1.7\r\n%âãÏÓ\nrest of file")
	r := &Reader{
		f: bytes.NewReader(blob),
	}
	got := r.headerVersion()
	assert.Equal(t, "1.7", got)

	// If no header present, expect empty string
	r2 := &Reader{f: bytes.NewReader([]byte("no pdf header here"))}
	assert.Equal(t, "", r2.headerVersion())
}
