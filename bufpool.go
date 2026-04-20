// Copyright © 2026, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: BSD-3-Clause

package xtract

import (
    "bytes"
    "sync"
)

var bytesBufferPool = sync.Pool{
    New: func() interface{} {
        return new(bytes.Buffer)
    },
}

func getBytesBuffer() *bytes.Buffer {
    return bytesBufferPool.Get().(*bytes.Buffer)
}

func putBytesBuffer(buf *bytes.Buffer) {
    buf.Reset()
    bytesBufferPool.Put(buf)
}
