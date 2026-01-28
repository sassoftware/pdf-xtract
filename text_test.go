// Copyright © 2026, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: BSD-3-Clause
package xtract

import (
	"testing"
	"unicode"

	"github.com/stretchr/testify/assert"
)

func TestIsPDFDocEncoded(t *testing.T) {
	// ASCII text (valid PDFDocEncoding)
	if !isPDFDocEncoded("Hello!") {
		t.Error(`Expected "Hello!" to be PDFDocEncoded`)
	}

	// UTF16 encoded string (returns false)
	utf16str := string([]byte{0xfe, 0xff, 0x00, 0x41}) // BOM + 'A'
	if isPDFDocEncoded(utf16str) {
		t.Error(`Expected UTF16 string to NOT be PDFDocEncoded`)
	}

	// Find a real unmapped byte from pdfDocEncoding
	var unmapped byte = 0xFF // fallback
	found := false
	for i := 0; i < 256; i++ {
		if pdfDocEncoding[byte(i)] == unicode.ReplacementChar {
			unmapped = byte(i)
			found = true
			break
		}
	}
	if !found {
		t.Skip("No unmapped byte found in pdfDocEncoding table")
	}

	if isPDFDocEncoded(string([]byte{unmapped})) {
		t.Errorf("Expected byte 0x%X to NOT be PDFDocEncoded", unmapped)
	}
}

func TestPdfDocDecode(t *testing.T) {
	s := "Hello!"
	decoded := pdfDocDecode(s)
	assert.Equal(t, s, decoded) // plain ASCII remains same

	// character outside ASCII but within pdfDocEncoding
	input := string([]byte{0x80}) // maps to 0x2022
	decoded2 := pdfDocDecode(input)
	assert.Equal(t, string([]rune{0x2022}), decoded2)
}

func TestIsUTF16(t *testing.T) {
	assert.True(t, isUTF16("\xfe\xff\x00\x41"))
	assert.False(t, isUTF16("Hello"))
	assert.False(t, isUTF16("\xfe\xff\x00")) // odd length
}

func TestUtf16Decode(t *testing.T) {
	// UTF16BE for 'A' (0x0041) and 'B' (0x0042)
	input := string([]byte{0x00, 0x41, 0x00, 0x42})
	output := utf16Decode(input)
	assert.Equal(t, "AB", output)
}

func TestDecodeUTF8OrPreserve(t *testing.T) {
	valid := "Hello"
	runes := DecodeUTF8OrPreserve(valid)
	assert.Equal(t, []rune{'H', 'e', 'l', 'l', 'o'}, runes)

	invalid := string([]byte{0xff, 0xfe}) // invalid UTF8 bytes
	runes2 := DecodeUTF8OrPreserve(invalid)
	assert.Equal(t, []rune{0xff, 0xfe}, runes2) // preserved as-is
}

func TestIsSameSentence(t *testing.T) {
	last := Text{Font: "Arial", FontSize: 12, Y: 100, S: "Hello"}
	current := Text{Font: "Arial", FontSize: 12.05, Y: 102, S: "world"}
	assert.True(t, IsSameSentence(last, current))

	// Different font -> not same sentence
	current2 := Text{Font: "Times", FontSize: 12, Y: 100, S: "Hello"}
	assert.False(t, IsSameSentence(last, current2))

	// Empty last segment -> not same sentence
	lastEmpty := Text{Font: "Arial", FontSize: 12, Y: 100, S: ""}
	assert.False(t, IsSameSentence(lastEmpty, current))
}
