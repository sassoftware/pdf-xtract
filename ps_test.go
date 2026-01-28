// Copyright © 2026, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: BSD-3-Clause

package xtract

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStack(t *testing.T) {
	var stk Stack
	v1 := Value{}
	v2 := Value{}

	stk.Push(v1)
	stk.Push(v2)
	assert.Equal(t, 2, stk.Len(), "expected Len()=2 after pushing two elements")

	popped := stk.Pop()
	assert.Equal(t, v2, popped, "expected last pushed value to be popped first")

	popped = stk.Pop()
	assert.Equal(t, v1, popped, "expected second pop to return the first pushed value")

	empty := stk.Pop()
	assert.Equal(t, (Value{}), empty, "popping empty stack should return zero Value")
}

func TestBuffer_seekForward(t *testing.T) {
	b := newBuffer(bytes.NewReader([]byte("hello world")), 0)
	b.seekForward(5)
	assert.True(t, b.offset >= 5)
	assert.True(t, b.pos >= 0)
}
