// Copyright © 2026, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: BSD-3-Clause

package xtract

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name      string
		cfg       *Config
		shouldErr bool
	}{
		{
			name: "valid config",
			cfg: &Config{
				MaxConcurrentPDFs: 10,
				MaxWorkersPerPDF:  2,
				WorkerTimeout:     5 * time.Second,
				ParsingMode:       BestEffort,
				MaxRetries:        1,
			},
			shouldErr: false,
		},
		{
			name: "invalid MaxConcurrentPDFs (too low)",
			cfg: &Config{
				MaxConcurrentPDFs: 0,
				MaxWorkersPerPDF:  2,
				WorkerTimeout:     5 * time.Second,
				ParsingMode:       BestEffort,
				MaxRetries:        1,
			},
			shouldErr: true,
		},
		{
			name: "invalid MaxWorkersPerPDF (too low)",
			cfg: &Config{
				MaxConcurrentPDFs: 10,
				MaxWorkersPerPDF:  0,
				WorkerTimeout:     5 * time.Second,
				ParsingMode:       Strict,
				MaxRetries:        1,
			},
			shouldErr: true,
		},
		{
			name: "missing WorkerTimeout",
			cfg: &Config{
				MaxConcurrentPDFs: 10,
				MaxWorkersPerPDF:  2,
				WorkerTimeout:     0,
				ParsingMode:       BestEffort,
				MaxRetries:        1,
			},
			shouldErr: true,
		},
		{
			name: "invalid ParsingMode",
			cfg: &Config{
				MaxConcurrentPDFs: 10,
				MaxWorkersPerPDF:  2,
				WorkerTimeout:     5 * time.Second,
				ParsingMode:       "invalid-mode",
				MaxRetries:        1,
			},
			shouldErr: true,
		},
		{
			name: "invalid MaxRetries (too high)",
			cfg: &Config{
				MaxConcurrentPDFs: 10,
				MaxWorkersPerPDF:  2,
				WorkerTimeout:     5 * time.Second,
				ParsingMode:       BestEffort,
				MaxRetries:        10,
			},
			shouldErr: true,
		},
		{
			name:      "default config is valid",
			cfg:       NewDefaultConfig(),
			shouldErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.shouldErr {
				assert.Error(t, err, "expected validation error")
			} else {
				assert.NoError(t, err, "expected validation to pass")
			}
		})
	}
}
