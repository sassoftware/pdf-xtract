// Copyright © 2026, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: BSD-3-Clause

package logger

import (
	"github.com/sassoftware/pdf-xtract/tracer"
)

// LogLevel represents log severity
type LogLevel string

const (
	DebugLevel LogLevel = "debug"
	ErrorLevel LogLevel = "error"
)

// LogFunc is a single logger function that handles all levels
type LogFunc func(level LogLevel, msg string, keyvals ...interface{})

var logFunc LogFunc = func(level LogLevel, msg string, keyvals ...interface{}) {
}

// SetLogger sets the global logger function
func SetLogger(f LogFunc) {
	if f != nil {
		logFunc = f
	}
}

// Debug logs a message at debug level
// If the last keyvals element is a bool and true, it is treated as trace flag
func Debug(msg string, keyvals ...interface{}) {
	trace := false
	if len(keyvals) > 0 {
		if b, ok := keyvals[len(keyvals)-1].(bool); ok {
			trace = b
			keyvals = keyvals[:len(keyvals)-1]
		}
	}
	logFunc(DebugLevel, msg, keyvals...)

	if trace {
		tracer.Log(msg)
	}
}

// Error logs a message at error level
func Error(msg string, keyvals ...interface{}) {
	logFunc(ErrorLevel, msg, keyvals...)
}
