# pdf-xtract

## Overview

Go-based PDF processing library providing high-fidelity text, content, and metadata extraction capabilities.

Originally forked from [ledongthuc/pdf](https://github.com/ledongthuc/pdf), this library has been extensively refactored to meet enterprise-grade observability, performance, and compliance requirements.  

- Efficient parsing and extraction of plain text, structured content, and document metadata
- Robust logging and tracing instrumentation for production debugging
- Compatibility with PDF v1.4 to v2.0 standards

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
- Error logs include contextual information (file, object, and parsing state).

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
// Print metadata as pretty JSON to stdout
err := proc.Metadata(ctx, "yourfile.pdf", os.Stdout)
if err != nil {
	fmt.Println("Failed to extract metadata:", err)
}

```

### CPU and Memory Usage Comparison (Batch vs Streaming)

| PDF Size (KB) | Batch mode CPU % | Batch mode  Memory % | Streaming mode CPU % | Streaming mode Memory % | PDF Characteristics |
|---:|---:|---:|---:|---:|---|
| 1 | 0.491 | 10.0 | 0.657 | 7.15 | 2-page PDF 1.7, multiline text streams per page, Type1 Helvetica, hybrid compressed XRef stream |
| 2 | 0.880 | 10.3 | 0.811 | 7.18 | Minimal PDF 2.0, single text stream, XMP metadata, hybrid XRef stream with /Prev (incremental update) |
| 3 | 0.675 | 10.0 | 0.383 | 7.29 | 5-page PDF 1.7, Flate-compressed streams per page, Info dictionary metadata |
| 4 | 2.290 | 9.97 | 0.373 | 0.73 | 1-page PDF 1.7, multiple streams, multiple fonts, transformations (rotate/scale) |
| 23 | 0.655 | 8.70 | 0.520 | 7.38 | Excel-to-PDF, tagged structure, /Lang, XMP and Info dictionary |
| 41 | 0.258 | 10.0 | 0.479 | 6.45 | Layout-heavy PDF (region/box-based content), visual-order extraction required |
| 121 | 2.190 | 9.69 | 0.612 | 0.849 | PowerPoint-to-PDF, multi-slide pages, absolute positioned layout |
| 190 | 2.010 | 9.60 | 0.532 | 8.29 | Linearized PDF 1.6, compressed XRef, /Prev incremental updates |
| 221 | 3.010 | 11.3 | 1.050 | 9.21 | 15-page multilingual (CJK and English), CID fonts, ToUnicode CMaps |
| 1382 | 3.930 | 12.4 | 4.160 | 11.4 | Large multi-page PDF (83 pages), dense legislative text |
| 3884 | 2.280 | 14.0 | 1.750 | 11.9 | Mixed text and embedded images, image-heavy pages |
| 5939 | 100 | 23.0 | 100 | 20.4 | Extremely large PDF (~1000+ pages), CPU saturation during extraction |

## Contributing
Maintainers are accepting patches and contributions to this project.
Please read [CONTRIBUTING.md](CONTRIBUTING.md) for details about submitting contributions to this project.

## License
This project is licensed under the [BSD 3-Clause License](LICENSE).
