package alloc

import (
	"sync"
	"testing"
	
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlab_AllocAndReset(t *testing.T) {
	s := &slab{buf: make([]byte, 64)}

	ptr := s.alloc(16)
	require.NotNil(t, ptr)
	assert.Equal(t, 16, s.offset)

	ptr2 := s.alloc(32)
	require.NotNil(t, ptr2)
	assert.GreaterOrEqual(t, s.offset, 48)

	// overflow
	ptr3 := s.alloc(100)
	assert.Nil(t, ptr3)

	// reset
	s.reset()
	assert.Equal(t, 0, s.offset)
}

func TestAllocator_NewAndRawAlloc(t *testing.T) {
	a := New()

	ptr := a.RawAlloc(32)
	require.NotNil(t, ptr)

	stats := a.Stats()
	assert.Equal(t, int64(32), stats.BytesAllocated)
	assert.Equal(t, int64(1), stats.ObjectsAllocated)
	assert.Equal(t, int64(1), stats.SlabsAllocated)
}

func TestAllocator_GrowAndAlloc(t *testing.T) {
	a := NewSize(64)

	ptr := a.RawAlloc(1024) 
	require.NotNil(t, ptr)

	stats := a.Stats()
	assert.GreaterOrEqual(t, stats.SlabsAllocated, int64(2))
}

func TestAllocator_MultipleSlabs(t *testing.T) {
	a := NewSize(64)
	a.RawAlloc(50)
	a.RawAlloc(50) 
	count := 0
	for s := a.head; s != nil; s = s.next {
		count++
	}
	assert.GreaterOrEqual(t, count, 2)
}

func TestAllocator_Reset(t *testing.T) {
	a := New()

	a.RawAlloc(100)
	a.RawAlloc(200)

	before := a.BytesInUse()
	require.Greater(t, before, int64(0))

	a.Reset()

	after := a.BytesInUse()
	assert.Equal(t, int64(0), after)

	stats := a.Stats()
	assert.Equal(t, int64(1), stats.Resets)
}

func TestAllocator_InvalidAllocPanics(t *testing.T) {
	a := New()

	assert.Panics(t, func() { a.RawAlloc(0) })
	assert.Panics(t, func() { a.RawAlloc(-1) })
}

func TestAllocator_GetSlabFromPool(t *testing.T) {
	a := NewSize(128)
	a.RawAlloc(200)
	a.Reset()
	stats := a.Stats()
	assert.GreaterOrEqual(t, stats.SlabsAllocated, int64(1))
}

func TestAllocatorPool(t *testing.T) {
	pool := NewPool(128)
	a := pool.Get()
	require.NotNil(t, a)
	a.RawAlloc(100)
	pool.Return(a)
	a2 := pool.Get()
	assert.Equal(t, int64(0), a2.BytesInUse())
}

func TestAlloc_Generic(t *testing.T) {
	a := New()
	v := Alloc[int](a)
	require.NotNil(t, v)
	*v = 99
	assert.Equal(t, 99, *v)
}

func TestAllocSlice_Generic(t *testing.T) {
	a := New()

	s := AllocSlice[int](a, 10)
	require.Len(t, s, 10)

	s[0] = 42
	assert.Equal(t, 42, s[0])
}

func TestBoundedAllocator(t *testing.T) {
	ba := NewBounded(1024)

	ptr, err := BoundedAlloc[int](ba)
	require.NoError(t, err)
	require.NotNil(t, ptr)

	used, limit := ba.Usage()
	assert.Greater(t, used, int64(0))
	assert.Equal(t, int64(1024), limit)
}

func TestBoundedAllocatorLimit(t *testing.T) {
	ba := NewBounded(8)

	_, err := BoundedAllocSlice[int64](ba, 2) // 16 bytes
	require.Error(t, err)
	assert.Contains(t, err.Error(), "memory limit exceeded")
}

func TestBoundedAllocatorRemaining(t *testing.T) {
	ba := NewBounded(100)

	_, _ = BoundedAlloc[int](ba)

	remaining := ba.Remaining()
	assert.Less(t, remaining, int64(100))
}

func TestBoundedAllocatorReset(t *testing.T) {
	ba := NewBounded(100)

	_, _ = BoundedAlloc[int](ba)

	ba.Reset()

	used, _ := ba.Usage()
	assert.Equal(t, int64(0), used)
}

func TestBoundedAllocatorStats(t *testing.T) {
	ba := NewBounded(100)

	_, _ = BoundedAlloc[int](ba)

	stats := ba.Stats()
	assert.GreaterOrEqual(t, stats.ObjectsAllocated, int64(1))
}

func TestBoundedAllocator_ConcurrentSafety(t *testing.T) {
	ba := NewBounded(1024 * 1024)

	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = BoundedAllocSlice[int](ba, 50)
		}()
	}

	wg.Wait()

	used, _ := ba.Usage()
	assert.Greater(t, used, int64(0))
}

func TestSlabPool(t *testing.T) {
	pool := &slabPool{bySize: make(map[int][]*slab)}

	s := &slab{buf: make([]byte, 128)}
	pool.put(s)

	got := pool.get(128)
	require.NotNil(t, got)
	assert.Equal(t, 0, got.offset)

	// empty case
	got2 := pool.get(128)
	assert.Nil(t, got2)
}

func TestNextPow2(t *testing.T) {
	assert.Equal(t, 1, nextPow2(1))
	assert.Equal(t, 2, nextPow2(2))
	assert.Equal(t, 4, nextPow2(3))
	assert.Equal(t, 8, nextPow2(5))
}

func TestClampSlabSize(t *testing.T) {
	assert.Equal(t, 64, clampSlabSize(1))
	assert.Equal(t, 128, clampSlabSize(100))
	assert.LessOrEqual(t, clampSlabSize(MaxSlabSize*2), MaxSlabSize)
}

func TestSlab_Alignment(t *testing.T) {
	s := &slab{buf: make([]byte, 64)}

	ptr := s.alloc(1)
	addr := uintptr(ptr)

	assert.Equal(t, uintptr(0), addr%uintptr(ptrSize))
}