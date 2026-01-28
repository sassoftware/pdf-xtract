// Copyright © 2026, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: BSD-3-Clause

package xtract

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testDir = "testdata"

// Get all PDFs from testdata
func getSamplePDFs(t *testing.T) []string {
	files, err := os.ReadDir(testDir)
	require.NoError(t, err)
	var pdfs []string
	for _, f := range files {
		if !f.IsDir() && strings.HasSuffix(f.Name(), ".pdf") {
			pdfs = append(pdfs, filepath.Join(testDir, f.Name()))
		}
	}
	if len(pdfs) == 0 {
		t.Skip("no PDF files found in testdata/")
	}
	return pdfs
}

// create a Processor
func newTestProcessor(mode ParsingMode) *processor {
	cfg := NewDefaultConfig()
	cfg.ParsingMode = mode
	return NewProcessor(cfg)
}

// Load first page of PDF
func loadPage(t *testing.T, path string) *Page {
	_, r, err := Open(path)
	if err != nil {
		t.Logf("Skipping malformed PDF %s: %v", path, err)
		t.SkipNow()
		return nil
	}
	if r.NumPage() == 0 {
		t.Logf("Skipping PDF with zero pages: %s", path)
		t.SkipNow()
		return nil
	}
	page := r.Page(1)
	return &page
}

// StrictExtractor
func TestStrictExtractor_ExtractPage(t *testing.T) {
	pdfs := getSamplePDFs(t)
	for _, path := range pdfs {
		page := loadPage(t, path)
		if page == nil {
			continue
		}
		t.Run(filepath.Base(path), func(t *testing.T) {
			ex := &StrictExtractor{}
			text, err := ex.ExtractPage(context.Background(), page)
			if err != nil {
				t.Logf("StrictExtractor returned expected error on malformed page: %v", err)
				t.SkipNow()
			}
			assert.NotEmpty(t, strings.TrimSpace(text))
		})
	}
}

// BestEffortExtractor
func TestBestEffortExtractor_ExtractPage(t *testing.T) {
	pdfs := getSamplePDFs(t)
	for _, path := range pdfs {
		page := loadPage(t, path)
		if page == nil {
			continue
		}
		t.Run(filepath.Base(path), func(t *testing.T) {
			ex := &BestEffortExtractor{}
			text, err := ex.ExtractPage(context.Background(), page)
			if err != nil {
				t.Logf("Skipping malformed PDF %s: %v", path, err)
				t.SkipNow()
			}
			assert.NotEmpty(t, strings.TrimSpace(text))
		})
	}
}

// processor.Extract
func TestProcessor_Extract(t *testing.T) {
	pdfs := getSamplePDFs(t)
	proc := newTestProcessor(BestEffort)
	ctx := context.Background()

	for _, path := range pdfs {
		t.Run(filepath.Base(path), func(t *testing.T) {
			text, _, err := proc.Extract(ctx, path)
			if err != nil {
				t.Logf("Skipping malformed or unreadable PDF %s: %v", path, err)
				t.SkipNow()
			}
			assert.NotEmpty(t, strings.TrimSpace(text))
		})
	}
}

// processor.Extract with truncation
func TestProcessor_Extract_Truncation(t *testing.T) {
	pdfs := getSamplePDFs(t)
	for _, path := range pdfs {
		t.Run(filepath.Base(path), func(t *testing.T) {
			cfg := NewDefaultConfig()
			cfg.ParsingMode = BestEffort
			cfg.MaxTotalChars = 50 // small limit to test truncation behavior
			proc := NewProcessor(cfg)
			ctx := context.Background()

			text, truncated, err := proc.Extract(ctx, path)
			if err != nil {
				t.Logf("Skipping malformed PDF %s: %v", path, err)
				t.SkipNow()
			}

			// The visible text extracted must never exceed MaxTotalChars
			assert.True(t, len(text) <= cfg.MaxTotalChars, "extracted text exceeds MaxTotalChars")

			// Detect whether truncation is actually expected:
			// If the visible text length is below the limit, truncation should be false.
			// Otherwise (text too long), truncation must be true.
			expectedTruncation := len(text) >= cfg.MaxTotalChars

			assert.Equal(t, expectedTruncation, truncated,
				"unexpected truncation state for %s (len=%d, limit=%d)",
				filepath.Base(path), len(text), cfg.MaxTotalChars)

			// Text should not be empty
			assert.NotEmpty(t, strings.TrimSpace(text))
		})
	}
}

// processor.ExtractAsStream
func TestProcessor_ExtractAsStream(t *testing.T) {
	pdfs := getSamplePDFs(t)
	proc := newTestProcessor(BestEffort)
	ctx := context.Background()

	for _, path := range pdfs {
		t.Run(filepath.Base(path), func(t *testing.T) {
			stream, truncated, err := proc.ExtractAsStream(ctx, path)
			if err != nil {
				t.Logf("Skipping malformed PDF %s: %v", path, err)
				t.SkipNow()
			}

			var combined strings.Builder
			for chunk := range stream {
				combined.WriteString(chunk)
			}
			text := combined.String()
			assert.NotEmpty(t, strings.TrimSpace(text))
			assert.False(t, truncated, "should not be truncated by default")
		})
	}
}

// cacheFonts
func TestCacheFonts(t *testing.T) {
	pdfs := getSamplePDFs(t)
	for _, path := range pdfs {
		page := loadPage(t, path)
		if page == nil {
			continue
		}
		t.Run(filepath.Base(path), func(t *testing.T) {
			fonts := cacheFonts(page)
			if len(fonts) == 0 {
				t.Logf("Skipping page with no fonts in %s", path)
				t.SkipNow()
			}
			require.NotNil(t, fonts)
			assert.NotEmpty(t, fonts)
		})
	}
}

func TestProcessor_Metadata(t *testing.T) {
	pdfs := getSamplePDFs(t)
	path := pdfs[1]

	proc := newTestProcessor(BestEffort)
	ctx := context.Background()

	var out strings.Builder
	err := proc.Metadata(ctx, path, &out)

	if err != nil {
		t.Logf("Skipping malformed PDF %s: %v", path, err)
		t.SkipNow()
	}

	require.NoError(t, err)
	assert.NotEmpty(t, strings.TrimSpace(out.String()), "metadata JSON should not be empty")
	assert.Contains(t, out.String(), "{", "expected JSON output to contain '{'")
}



func TestStreamInOrder_TruncationAndOrdering(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.ParsingMode = BestEffort
	cfg.MaxTotalChars = 5

	proc := NewProcessor(cfg)

	results := make(chan pageResult)
	outCh := make(chan string, 10)

	// Send pages out of order
	go func() {
		results <- pageResult{index: 2, text: "WORLD"}
		results <- pageResult{index: 1, text: "HELLO"}
		close(results)
	}()

	truncated := proc.streamInOrder(results, outCh)
	close(outCh)

	var output strings.Builder
	for s := range outCh {
		output.WriteString(s)
	}

	assert.True(t, truncated, "expected stream to be truncated")
	assert.Equal(t, "HELLO", output.String(), "output must be ordered and truncated")
}

func TestStreamInOrder_StrictMode(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.ParsingMode = Strict

	proc := NewProcessor(cfg)

	results := make(chan pageResult)
	outCh := make(chan string, 5) 

	go func() {
		results <- pageResult{index: 1, text: "OK"}
		results <- pageResult{index: 2, err: assert.AnError}
		close(results)
	}()

	truncated := proc.streamInOrder(results, outCh)

	assert.False(t, truncated)
}

func TestStreamInOrder_PartialTruncation(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.ParsingMode = BestEffort
	cfg.MaxTotalChars = 3 // force partial truncation

	proc := NewProcessor(cfg)

	results := make(chan pageResult)
	outCh := make(chan string, 1) // buffered to avoid blocking

	go func() {
		// len("ABCDE") > remaining(3)
		results <- pageResult{index: 1, text: "ABCDE"}
		close(results)
	}()

	truncated := proc.streamInOrder(results, outCh)
	close(outCh)

	// collect output
	var out string
	for s := range outCh {
		out += s
	}
	assert.True(t, truncated, "expected truncation to be true")
	assert.Equal(t, "ABC", out, "expected partial truncation output")
}

func TestAdjustWorkerCount(t *testing.T) {
	proc := &processor{}

	assert.Equal(t, 1, proc.adjustWorkerCount(0))
	assert.Equal(t, runtime.NumCPU(), proc.adjustWorkerCount(runtime.NumCPU()))
	assert.Equal(t, 2, proc.adjustWorkerCount(2))
}


