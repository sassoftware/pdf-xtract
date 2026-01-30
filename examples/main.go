// Copyright © 2026, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	xtract "github.com/sassoftware/pdf-xtract"
	"github.com/sassoftware/pdf-xtract/logger"
	"github.com/sassoftware/pdf-xtract/tracer"
)

func main() {
	cfg := xtract.NewDefaultConfig()
	cfg.MaxConcurrentPDFs = 1
	cfg.MaxWorkersPerPDF = 4
	cfg.ParsingMode = xtract.BestEffort
	cfg.MaxTotalChars = 5000
	cfg.Logger = func(level logger.LogLevel, msg string, keyvals ...interface{}) {
		// no-op logger
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	proc := xtract.NewProcessor(cfg)

	// Example 1: Extract full text (with maxChars)
	const path = "../testdata/pdf_test.pdf"

	text, truncated, err := proc.Extract(ctx, path)
	if err != nil {
		tracer.Flush()
		return
	}
	fmt.Println("Truncated?", truncated)
	fmt.Println("Text:", text)

	fmt.Println("---- PDF Metadata ----")
	if err := proc.Metadata(ctx, path, os.Stdout); err != nil {
		tracer.Flush()
		return
	}

	// Example 2: Streaming extraction

	// stream, truncated, err := proc.ExtractAsStream(ctx, "../../testdata/NC_Soil_Report.pdf")
	// if err != nil {
	// 	return
	// }

	// fmt.Println("Streaming output:")
	// var total string
	// for pageText := range stream {
	// 	fmt.Println("Page received:")
	// 	fmt.Println(pageText)
	// 	total += pageText
	// }
	// fmt.Println("Truncated?", truncated)
	// fmt.Println("Final concatenated length:", len(total))
}
