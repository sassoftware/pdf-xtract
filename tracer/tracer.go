// Copyright © 2026, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: BSD-3-Clause

package tracer

import (
	"fmt"
)

var traceMessages []string

// Log just adds a message to the trace log.
func Log(msg string) {
	traceMessages = append(traceMessages, msg)
}

// Flush prints the accumulated trace log and resets it.
func Flush() {
	for _, msg := range traceMessages {
		fmt.Println(msg)
	}
	// reset so the next run starts fresh
	traceMessages = nil
}
