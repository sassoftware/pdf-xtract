// Copyright © 2026, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: BSD-3-Clause

package xtract

import (
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/sassoftware/viya-pdf-xtract/logger"
)

type ParsingMode string

const (
	Strict     ParsingMode = "strict"
	BestEffort ParsingMode = "best-effort"
)

type Config struct {
	MaxConcurrentPDFs int           `validate:"min=1,max=10"`
	MaxWorkersPerPDF  int           `validate:"min=1,max=10"`
	WorkerTimeout     time.Duration `validate:"required"`
	ParsingMode       ParsingMode   `validate:"oneof=strict best-effort"`
	MaxRetries        int           `validate:"min=0,max=3"`
	MaxTotalChars     int           `validate:"min=0"`
	DebugOn           bool
	Logger            logger.LogFunc
	// Metrics           MetricsInterface
}

func NewDefaultConfig() *Config {
	return &Config{
		MaxConcurrentPDFs: 5,
		MaxWorkersPerPDF:  1,
		WorkerTimeout:     5 * time.Second,
		ParsingMode:       BestEffort,
		MaxRetries:        3,
		MaxTotalChars:     0,
		DebugOn:           false,
	}
}

func (cfg *Config) Validate() error {
	logger.Debug("Validating Config Object")
	validate := validator.New()
	return validate.Struct(cfg)
}
