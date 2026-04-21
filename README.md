# pdf-xtract

## Overview

Go-based PDF processing library providing high-fidelity text, content, and metadata extraction capabilities.

Originally forked from [ledongthuc/pdf](https://github.com/ledongthuc/pdf), this library has been extensively refactored to meet enterprise-grade observability, performance, and compliance requirements.  

- Efficient parsing and extraction of plain text, structured content, and document metadata.
- Robust logging and tracing instrumentation for production debugging.
- Compatibility with PDF v1.4 to v2.0 standards.

## Installation

You can install the library using Go modules:

`go get -u github.com/sassoftware/pdf-xtract`

### Getting Started

Import the library in your Go code:

`import github.com/sassoftware/pdf-xtract`

### Logging & Observability

 `import "github.com/sassoftware/pdf-xtract/logger"`

The refactored library includes a structured logging layer and a lightweight tracer interface to ensure production-grade observability.

- High-level structured logs added at major functional boundaries.
- Error logs include contextual information (file, object, and parsing state)

### Tracer Integration

`import "github.com/sassoftware/pdf-xtract/tracer"`

The library includes a lightweight Tracer subsystem that provides fine-grained observability into PDF parsing and extraction operations. It is designed to support debugging and operational monitoring in production environments where PDFs may be large, malformed, or complex.
 - Object-level processing times (fonts, content streams, metadata objects, etc.)
 - Recovery attempts (e.g., corrupted xref tables, missing object references)
 - Execution flow, enabling reconstruction of what happened during extraction
 - Error points, with the ability to dump the trace when failures occur

### Running

After installing the library, you can either integrate pdf-xtract into your own Go applications or run the provided example programs to get started quickly.

```sh
git clone https://github.com/sassoftware/pdf-xtract.git
cd pdf-xtract
cd examples
go run main.go
```
### Usage 

 - Check in examples
 - This library supports two primary extraction modes depending on the use case and PDF size:

	1. Standard Extraction Mode (Batch Mode) – best for small/medium PDFs, returns complete text at once
	2. Streaming Extraction Mode – best for large PDFs, returns text page-by-page without loading the entire file into memory


#### Standard Extraction Mode (Batch)

```golang
cfg := xtract.NewDefaultConfig()
cfg.MaxConcurrentPDFs = 1
cfg.MaxWorkersPerPDF = 4
cfg.ParsingMode = xtract.BestEffort
cfg.MaxTotalChars = 1000
cfg.MaxMemoryPerPDF = 10 << 20

cfg.Logger = func(level logger.LogLevel, msg string, keyvals ...interface{}) {
	// no-op logger
}

proc := xtract.NewProcessor(cfg)

text, truncated, err := proc.Extract(ctx, "pdf_test.pdf")
if err != nil {
	tracer.PrintTrace()
	return
}

fmt.Println("Truncated?", truncated)
fmt.Println("Extracted Text:", text)

// Metadata extraction
fmt.Println("---- PDF Metadata ----")
if err := proc.Metadata(ctx, "pdf_test.pdf", os.Stdout); err != nil {
	tracer.PrintTrace()
}

```

#### Streaming Extraction Mode

```golang
stream, truncated, err := proc.ExtractAsStream(ctx, "pdf_test.pdf")
if err != nil {
	return
}

fmt.Println("Streaming output:")
var total string

for pageText := range stream {
	fmt.Println(pageText)
	total += pageText
}

fmt.Println("Truncated?", truncated)
fmt.Println("Final concatenated length:", len(total))
```
#### Metadata Extraction
```golang
// Print metadata as JSON
err := proc.Metadata(ctx, "yourfile.pdf", os.Stdout)
if err != nil {
	fmt.Println("Failed to extract metadata:", err)
}

```

### Performance Improvements

Recent optimizations have improved extraction performance across all PDF sizes:

| PDF Size | Pages | Extraction Time (Before Changes) | Execution Time (After Changes) |
|---:|---:|---:|---:|
| 21 KB | 1 | 1.18s | 1.36s |
| 260 KB | 46 | 6.2s | 1.26s |
| 350 KB | 70 | 8.7s | 1.39s |
| 800 KB | 148 | 5.3s | 1.43s |
| 5 MB | 9 | 41s | 0.30s |
| 5.9 MB | 1000 | 5 min 12s | 7.5s |
| 6 MB | 772 | 5 min 18s | 5.36s |
| 11 MB | 602 | 5 min 25s | 10s | 

### Memory Usage per PDF

The library implements bounded memory allocation to prevent out-of-memory errors on large PDFs:

**Note:** Memory limits can be configured via `MaxMemoryPerPDF`. If the limit is exceeded during parsing, the operation will fail gracefully with a memory limit error.


## Contributing
Maintainers are accepting patches and contributions to this project.
Please read [CONTRIBUTING.md](CONTRIBUTING.md) for details about submitting contributions to this project.

## License
This project is licensed under the [BSD 3-Clause License](LICENSE).
