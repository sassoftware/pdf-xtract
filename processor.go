// Copyright © 2026, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: BSD-3-Clause

package xtract

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"

	"github.com/sassoftware/pdf-xtract/logger"
	"golang.org/x/sync/semaphore"
)

// Processor defines the contract for extracting text from a PDF file.
type Processor interface {
	Extract(ctx context.Context, path string) (string, bool, error)
}

// ExtractorStrategy defines how to extract text from a single page.
// Different strategies handle errors differently (strict vs. best-effort).
type ExtractorStrategy interface {
	ExtractPage(ctx context.Context, page *Page) (string, error)
}

// StrictExtractor enforces strict parsing.
// If any page fails, the entire extraction fails.
type StrictExtractor struct{}

func (s *StrictExtractor) ExtractPage(ctx context.Context, page *Page) (string, error) {
	fonts := cacheFonts(page)
	return page.GetPlainText(fonts)
}

// BestEffortExtractor tolerates errors.
// If a page fails, it simply skips that page.
type BestEffortExtractor struct{}

func (b *BestEffortExtractor) ExtractPage(ctx context.Context, page *Page) (string, error) {
	fonts := cacheFonts(page)
	text, err := page.GetPlainText(fonts)
	if err != nil {
		// In best-effort mode, ignore errors and continue.
		logger.Debug("BestEffortExtractor: failed to extract page text, ignoring error", "page", page, "err", err, true)
		return "", nil
	}
	return text, nil
}

// processor manages PDF extraction with concurrency control
// and delegates page-level work to the chosen ExtractorStrategy.
type processor struct {
	cfg       *Config
	sem       *semaphore.Weighted
	extractor ExtractorStrategy
}

// NewProcessor validates the config and creates a new processor.
// Selects the correct ExtractorStrategy (Strict or BestEffort).
func NewProcessor(cfg *Config) *processor {
	//Select ExtractorStrategy
	var extractor ExtractorStrategy
	switch cfg.ParsingMode {
	case Strict:
		extractor = &StrictExtractor{}
	case BestEffort:
		extractor = &BestEffortExtractor{}
	}

	//Validate the config object
	if err := cfg.Validate(); err != nil {
		panic(err)
	}

	//Set the logger function
	if cfg.Logger != nil {
		logger.SetLogger(cfg.Logger)
	}

	logger.Debug(fmt.Sprintf("Processor initialized: parsing_mode=%v, max_concurrent_pdfs=%d, max_workers_per_pdf=%d",
		cfg.ParsingMode, cfg.MaxConcurrentPDFs, cfg.MaxWorkersPerPDF), true)

	return &processor{
		cfg:       cfg,
		sem:       semaphore.NewWeighted(int64(cfg.MaxConcurrentPDFs)),
		extractor: extractor,
	}
}

// Extract extracts PDF text in order, respecting maxChars or Config.MaxTotalChars as a limit.
// Returns the full text (or up to the limit) and a truncated flag if the output hits the character limit.
func (p *processor) Extract(ctx context.Context, path string) (string, bool, error) {
	logger.Debug(fmt.Sprintf("Starting extraction: path=%s", path), true)

	if err := p.acquireSlot(ctx); err != nil {
		logger.Debug(fmt.Sprintf("Failed to acquire slot: err=%v", err), true)
		return "", false, err
	}
	defer p.sem.Release(1)
	logger.Debug(fmt.Sprintf("Slot acquired for extraction: path=%s", path), true)

	_, r, err := Open(path)
	if err != nil {
		logger.Debug(fmt.Sprintf("Failed to open PDF: path=%s err=%v", path, err), true)
		return "", false, err
	}

	total := r.NumPage()
	logger.Debug(fmt.Sprintf("Total pages detected: path=%s pages=%d", path, total), true)

	if total == 0 {
		logger.Debug(fmt.Sprintf("No pages found in PDF: path=%s", path), true)
		return "", false, nil
	}

	numWorkers := p.adjustWorkerCount(p.cfg.MaxWorkersPerPDF)
	logger.Debug(fmt.Sprintf("Starting workers: count=%d", numWorkers), true)

	jobs, results := make(chan int, total), make(chan pageResult, total)

	var wg sync.WaitGroup
	p.startWorkers(ctx, *r, jobs, results, numWorkers, &wg)
	p.feedJobs(ctx, total, jobs)
	close(jobs)

	// In-order collection with truncation

	go func() {
		wg.Wait()
		close(results)
	}()

	// Emit in-order pages immediately
	out, truncated, err := p.emitInOrder(results)
	if err != nil {
		return "", false, err
	}

	logger.Debug(fmt.Sprintf("Extraction completed: path=%s truncated=%v total_chars=%d", path, truncated, out.Len()), true)
	return out.String(), truncated, nil
}

// ExtractAsStream streams PDF text in order, respecting maxChars or Config.MaxTotalChars as a limit.
// Stops emitting further text once the effective character limit is reached, supporting unlimited extraction if limit is 0.
func (p *processor) ExtractAsStream(ctx context.Context, path string) (<-chan string, bool, error) {
	logger.Debug(fmt.Sprintf("Starting streaming extraction: path=%s", path), true)

	if err := p.acquireSlot(ctx); err != nil {
		logger.Debug(fmt.Sprintf("Failed to acquire slot for stream: err=%v", err), true)
		return nil, false, err
	}
	defer p.sem.Release(1)

	_, r, err := Open(path)
	if err != nil {
		logger.Debug(fmt.Sprintf("Failed to open PDF for streaming: err=%v", err), true)
		return nil, false, err
	}

	total := r.NumPage()
	logger.Debug(fmt.Sprintf("Streaming: total pages=%d", total), true)

	if total == 0 {
		ch := make(chan string)
		close(ch)
		return ch, false, nil
	}

	numWorkers := p.adjustWorkerCount(p.cfg.MaxWorkersPerPDF)
	jobs, results := make(chan int, total), make(chan pageResult, total)

	var wg sync.WaitGroup

	p.startWorkers(ctx, *r, jobs, results, numWorkers, &wg)
	p.feedJobs(ctx, total, jobs)
	close(jobs)

	outCh := make(chan string)
	truncated := false

	go func() {
		defer close(outCh)
		go func() {
			wg.Wait()
			close(results)
		}()
		truncated = p.streamInOrder(results, outCh)
		logger.Debug(fmt.Sprintf("Streaming extraction completed: path=%s truncated=%v", path, truncated), true)
	}()

	return outCh, truncated, nil
}

func (p *processor) emitInOrder(results chan pageResult) (strings.Builder, bool, error) {
	pageBuffer := make(map[int]string)
	nextPage := 1
	var out strings.Builder
	truncated := false
	for res := range results {
		if res.err != nil && p.cfg.ParsingMode == Strict {
			logger.Debug(fmt.Sprintf("Strict mode error — stopping extraction: page=%d err=%v", res.index, res.err))
			return out, false, fmt.Errorf("strict mode failed on page %d: %w", res.index, res.err)
		}
		pageBuffer[res.index] = res.text

		// Emit in-order pages immediately
		for {
			text, ok := pageBuffer[nextPage]
			if !ok || text == "" {
				break
			}

			// Only apply truncation logic if p.cfg.MaxTotalChars > 0
			if p.cfg.MaxTotalChars > 0 {
				remaining := p.cfg.MaxTotalChars - out.Len()
				if remaining <= 0 {
					truncated = true
					logger.Debug(fmt.Sprintf("Truncation reached: limit=%d", p.cfg.MaxTotalChars), true)
					break
				}
				if len(text) > remaining {
					out.WriteString(text[:remaining])
					truncated = true
					logger.Debug(fmt.Sprintf("Partial truncation applied: remaining=%d page=%d", remaining, nextPage), true)
				} else {
					out.WriteString(text)
				}
			} else {
				// No truncation limit → write full text
				out.WriteString(text)
			}

			delete(pageBuffer, nextPage)
			nextPage++

			if truncated {
				break
			}
		}
		if truncated {
			break
		}
	}
	return out, truncated, nil
}

func (p *processor) streamInOrder(results chan pageResult, outCh chan string) (truncated bool) {
	pageBuffer := make(map[int]string)
	nextPage := 1
	totalChars := 0

	for res := range results {
		if res.err != nil && p.cfg.ParsingMode == Strict {
			logger.Debug(fmt.Sprintf("Strict mode error — stopping streaming: page=%d err=%v", res.index, res.err), true)
			return false
		}
		pageBuffer[res.index] = res.text

		// Emit pages in-order
		for {
			text, ok := pageBuffer[nextPage]
			if !ok || text == "" {
				break
			}

			if p.cfg.MaxTotalChars > 0 {
				remaining := p.cfg.MaxTotalChars - totalChars
				if remaining <= 0 {
					truncated = true
					logger.Debug(fmt.Sprintf("Streaming truncation reached: limit=%d", p.cfg.MaxTotalChars), true)
					return truncated
				}
				if len(text) > remaining {
					outCh <- text[:remaining]
					totalChars += remaining
					truncated = true
					logger.Debug(fmt.Sprintf("Streaming partial truncation applied: remaining=%d page=%d", remaining, nextPage), true)
					return truncated
				}
				outCh <- text
				totalChars += len(text)
			} else {
				outCh <- text
				totalChars += len(text)
			}

			delete(pageBuffer, nextPage)
			nextPage++
		}
	}

	return truncated
}

func (p *processor) acquireSlot(ctx context.Context) error {
	if err := p.sem.Acquire(ctx, 1); err != nil {
		return fmt.Errorf("acquire slot: %w", err)
	}
	logger.Debug("Slot acquired successfully", true)
	return nil
}

func (p *processor) adjustWorkerCount(maxWorkers int) int {
	if maxWorkers < 1 {
		maxWorkers = 1
	}
	if maxWorkers > runtime.NumCPU()/2 {
		maxWorkers = runtime.NumCPU()
	}
	logger.Debug(fmt.Sprintf("Adjusted worker count: workers=%d", maxWorkers), true)
	return maxWorkers
}

type pageResult struct {
	index int
	text  string
	err   error
}

func (p *processor) startWorkers(ctx context.Context, r Reader, jobs <-chan int, results chan<- pageResult, numWorkers int, wg *sync.WaitGroup) {
	logger.Debug(fmt.Sprintf("Spawning workers: num_workers=%d", numWorkers), true)
	for w := 1; w <= numWorkers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			logger.Debug(fmt.Sprintf("Worker started: id=%d", id), true)
			for i := range jobs {
				page := r.Page(i)
				if page.V.IsNull() {
					logger.Debug(fmt.Sprintf("Null page encountered: index=%d", i), true)
					results <- pageResult{i, "", fmt.Errorf("null page")}
					continue
				}

				text, err := p.extractPageWithRetries(ctx, &page)
				results <- pageResult{i, text, err}
				if err != nil {
					logger.Debug(fmt.Sprintf("Worker: page extraction error: worker_id=%d page=%d err=%v", id, i, err), true)
				} else {
					logger.Debug(fmt.Sprintf("Worker: page extracted successfully: worker_id=%d page=%d", id, i), true)
				}
			}
			logger.Debug(fmt.Sprintf("Worker finished: id=%d", id), true)
		}(w)
	}
}

func (p *processor) extractPageWithRetries(ctx context.Context, page *Page) (string, error) {
	var text string
	var err error
	for attempt := 0; attempt <= p.cfg.MaxRetries; attempt++ {
		ctxPage, cancel := context.WithTimeout(ctx, p.cfg.WorkerTimeout)
		text, err = p.extractor.ExtractPage(ctxPage, page)
		cancel()
		if err == nil {
			break
		}
		logger.Debug(fmt.Sprintf("Retrying page extraction: attempt=%d err=%v", attempt, err), true)
	}
	return text, err
}

func (p *processor) feedJobs(ctx context.Context, total int, jobs chan<- int) error {
	for i := 1; i <= total; i++ {
		select {
		case <-ctx.Done():
			logger.Debug("Context cancelled while feeding jobs", true)
			return ctx.Err()
		case jobs <- i:
			logger.Debug(fmt.Sprintf("Job queued: page=%d", i), true)
		}
	}
	logger.Debug(fmt.Sprintf("All jobs queued: total_pages=%d", total), true)
	return nil
}

// cacheFonts creates a one-time map of fonts for a page to avoid
// repeatedly parsing font charmaps.
func cacheFonts(page *Page) map[string]*Font {
	fonts := make(map[string]*Font)
	for _, name := range page.Fonts() {
		if _, exists := fonts[name]; !exists {
			f := page.Font(name)
			fonts[name] = &f
			logger.Debug(fmt.Sprintf("Cached font: name=%s", name), true)
		}
	}
	return fonts
}

// Metadata prints PDF metadata as JSON to the provided writer
func (p *processor) Metadata(ctx context.Context, path string, w io.Writer) error {
	logger.Debug(fmt.Sprintf("Reading metadata: path=%s", path), true)

	_, r, err := Open(path)
	if err != nil {
		logger.Error("failed to open PDF for metadata:")
		return err
	}
	defer func() {
		if closer, ok := r.f.(io.Closer); ok {
			_ = closer.Close()
		}
	}()
	if err := r.MetadataJSON(w); err != nil {
		logger.Error("failed to read metadata")
		return err
	}

	logger.Debug(fmt.Sprintf("Metadata extraction completed: path=%s", path), true)
	return nil
}
