// Copyright © 2026, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: BSD-3-Clause

package xtract

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAlphaReader_Read(t *testing.T) {
	// Mixed input:
	//   indices: 0:'!' (valid) 1:'u' (valid) 2:'x' (invalid) 3:'y' (invalid)
	//            4:'z' (invalid) 5:'~' (tilde) 6:'>' (terminator) 7:'A' (after terminator)
	src := []byte("!uxyz~>A")
	r := newAlphaReader(bytes.NewReader(src))

	buf := make([]byte, len(src))
	n, err := r.Read(buf)

	assert.NoError(t, err)
	assert.Equal(t, len(src), n, "Read should return number of bytes read from underlying reader")

	// Expect valid ASCII85 bytes preserved at same indices
	assert.Equal(t, byte('!'), buf[0], "valid ASCII85 '!' should be preserved")
	assert.Equal(t, byte('u'), buf[1], "valid ASCII85 'u' should be preserved")

	// After first two bytes, invalid chars should be zeroed (and processing should stop at '~>')
	for i := 2; i < len(src); i++ {
		// positions 2..6 should be zero because 'x','y','z' are invalid and '~>' ends processing
		assert.Equalf(t, byte(0), buf[i], "expected buf[%d] to be zero (invalid or after terminator)", i)
	}
}
