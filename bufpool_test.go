// Copyright © 2026, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: BSD-3-Clause

package xtract

import (	
	"testing"
)

func TestBufferPool(t *testing.T) {
	// Test bytes.Buffer pooling.
	t.Run("BytesBuffer", func(t *testing.T) {
		buf1 := getBytesBuffer()
		if buf1 == nil {
			t.Fatal("getBytesBuffer returned nil")
		}

		buf1.WriteString("test data")
		if buf1.String() != "test data" {
			t.Errorf("expected 'test data', got '%s'", buf1.String())
		}

		// Return to pool
		putBytesBuffer(buf1)

		// Get again - should be reset
		buf2 := getBytesBuffer()
		if buf2.Len() != 0 {
			t.Errorf("expected buffer to be reset, got length %d", buf2.Len())
		}

		putBytesBuffer(buf2)
	})

}