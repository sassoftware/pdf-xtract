// Package alloc implements a region-based allocator
// - it reduces GC pressure
// - it allocates in bounded slabs and throws error instead of OOM-killing
//
// Out-of-scope for objects - os.File etc. that require Close(), undefined lifetimes and escape the current request/batch scope
package alloc

import (
	"errors"
	"fmt"
	"math/bits"
	"sync"
	"unsafe"
)

const (
	DefaultSlabSize = 64 << 10 // 64 KiB
	MaxSlabSize     = 32 << 20 // 32 MiB
	ptrSize         = int(unsafe.Sizeof(uintptr(0)))
	alignMask       = ptrSize - 1
)

//When you create an Allocator, it immediately allocates one 64 KiB slab.
//As you allocate objects, they're carved out of this slab. When the slab fills up, another is allocated.

// slab list — memory region
type slab struct {
	// Allocates 64 KiB from Go's heap
	buf    []byte //The actual memory buffer that holds allocated data
	offset int    //Current position in the buffer where the next allocation will start
	next   *slab  //Pointer to the next slab in a linked list

}

// alloc tries to allocate n bytes from the slab. It returns a pointer to the allocated memory or nil if there isn't enough space.
func (s *slab) alloc(n int) unsafe.Pointer {
	aligned := (s.offset + alignMask) &^ alignMask
	end := aligned + n
	if end > len(s.buf) {
		return nil
	}
	s.offset = end
	return unsafe.Pointer(&s.buf[aligned])
}

// reset clears the slab for reuse by resetting the offset to zero.
// This allows the same slab to be reused for future allocations without needing to allocate new memory.
func (s *slab) reset() {
	s.offset = 0
}

// for diagnostics during debugging

type Stats struct {
	SlabsAllocated   int64 // total slab
	SlabsFromPool    int64 // total slabs retrieved
	BytesAllocated   int64 // total bytes allocated
	ObjectsAllocated int64 // total calls for allocation
	Resets           int64 // number of calls to reset									
}

// not-safe to share between multiple goroutines.
// for concurrent use AllocatorPool.

// Allocator manages a linked list of slabs and serves allocation requests from them.
// Manages memory allocation for a single "scope" (like processing one request).
type Allocator struct {
	head     *slab // first allocation
	slabSize int
	pool     *slabPool // re-usable slabs
	stats    Stats
}

// create a new Allocator with the default slab size.
// The Allocator will immediately allocate one slab upon creation.
func New() *Allocator {
	return NewSize(DefaultSlabSize)
}

// NewSize creates a new Allocator with the specified slab size.
// The slab size is clamped to a reasonable range to ensure efficient memory usage and alignment.
func NewSize(size int) *Allocator {
	size = clampSlabSize(size)
	a := &Allocator{slabSize: size, pool: globalPool}
	a.head = a.getSlab(size)
	return a
}

func (a *Allocator) RawAlloc(n int) unsafe.Pointer {

	if n <= 0 {
		panic("alloc: cannot allocate zero or negative bytes")
	}
	if ptr := a.head.alloc(n); ptr != nil {
		a.stats.BytesAllocated += int64(n)
		a.stats.ObjectsAllocated++
		return ptr
	}
	// a new slab - rare path
	return a.growAndAlloc(n)
}

func (a *Allocator) growAndAlloc(n int) unsafe.Pointer {
	slabSz := a.slabSize
	if n > slabSz {
		slabSz = nextPow2(n)
		if slabSz > MaxSlabSize {
			slabSz = n
		}
	}

	s := a.getSlab(slabSz)
	s.next = a.head
	a.head = s

	ptr := s.alloc(n)
	a.stats.BytesAllocated += int64(n)
	a.stats.ObjectsAllocated++
	return ptr
}

// Reset releases all slabs back to the pool and resets the allocator to its initial state with a single slab.
func (a *Allocator) Reset() {
	a.stats.Resets++
	for s := a.head; s != nil; {
		next := s.next
		s.next = nil
		a.pool.put(s)
		s = next
	}
	a.head = a.getSlab(a.slabSize)
}

// BytesInUse returns the total number of bytes currently allocated across all slabs managed by the Allocator.
func (a *Allocator) BytesInUse() int64 {
	var total int64
	for s := a.head; s != nil; s = s.next {
		total += int64(s.offset)
	}
	return total
}

func (a *Allocator) Stats() Stats { return a.stats }

// getSlab tries to retrieve a slab of the specified size from the pool.
// If none are available, it allocates a new one.
func (a *Allocator) getSlab(size int) *slab {
	if s := a.pool.get(size); s != nil {
		a.stats.SlabsFromPool++
		return s
	}
	a.stats.SlabsAllocated++
	return &slab{buf: make([]byte, size)}
}

// Generics ---
// Alloc allocates a single zero-initialized value of type T and returns a pointer.

func Alloc[T any](a *Allocator) *T {
	var zero T
	return (*T)(a.RawAlloc(int(unsafe.Sizeof(zero))))
}

// Example:
//
//	buf := alloc.AllocSlice[byte](a, 4096)
//	copy(buf, inputData)
// 	AllocSlice allocates a slice of n zero-initialized T values backed by the allocator.

func AllocSlice[T any](a *Allocator, n int) []T {
	if n == 0 {
		return nil
	}
	var zero T
	elemSize := int(unsafe.Sizeof(zero))
	ptr := a.RawAlloc(elemSize * n)
	return unsafe.Slice((*T)(ptr), n)
}

var ErrMemoryLimitExceeded = errors.New("alloc: memory limit exceeded")

// Example:
//
//	ba := alloc.NewBounded(256 << 20)  // 256 MiB hard ceiling
//	defer ba.Reset()
//
//	node, err := alloc.BoundedAlloc[MyNode](ba)
//	if errors.Is(err, alloc.ErrMemoryLimitExceeded) {
//	    return fmt.Errorf("document too large: %w", err)
//	}
// BoundedAllocator - This gives us deterministic worst-case memory consumption per request, which is critical in multi-tenant systems (parsers, e.g. pdf-extract).
// Thread-safe for concurrent use by multiple goroutines.

type BoundedAllocator struct {
	mu        sync.Mutex
	inner     *Allocator
	limit     int64
	allocated int64
}

// NewBounded creates a BoundedAllocator with the given memory limits in bytes.
func NewBounded(limitBytes int64) *BoundedAllocator {
	return &BoundedAllocator{inner: New(), limit: limitBytes}
}

// NewBoundedSize creates a BoundedAllocator
func NewBoundedSize(limitBytes int64, slabSize int) *BoundedAllocator {
	return &BoundedAllocator{inner: NewSize(slabSize), limit: limitBytes}
}

// Reset releases all memory and resets the allocation counter to zero.
// This is thread-safe and can be called from any goroutine.
// After Reset(), the allocator is ready for reuse with a fresh memory budget.
//
// WARNING: Calling Reset() invalidates all slices previously allocated via BoundedAllocSlice.
// Do not access any data obtained from this allocator after calling Reset().
func (ba *BoundedAllocator) Reset() {
	ba.mu.Lock()
	defer ba.mu.Unlock()
	ba.allocated = 0
	ba.inner.Reset()
}

// Usage returns current bytes allocated and the limit.
func (ba *BoundedAllocator) Usage() (bytesAllocated, limitBytes int64) {
	ba.mu.Lock()
	defer ba.mu.Unlock()
	return ba.allocated, ba.limit
}

// Remaining returns how many bytes can still be allocated before hitting the ceiling.
func (ba *BoundedAllocator) Remaining() int64 {
	ba.mu.Lock()
	defer ba.mu.Unlock()
	return ba.limit - ba.allocated
}

// Stats returns statistics from the inner allocator.
func (ba *BoundedAllocator) Stats() Stats {
	ba.mu.Lock()
	defer ba.mu.Unlock()
	return ba.inner.Stats()
}

// BoundedAlloc allocates a single T via the BoundedAllocator.
// Returns ErrMemoryLimitExceeded if the ceiling would be breached.
// Thread-safe for concurrent use.
func BoundedAlloc[T any](ba *BoundedAllocator) (*T, error) {
	ba.mu.Lock()
	defer ba.mu.Unlock()

	var zero T
	size := int64(unsafe.Sizeof(zero))
	if ba.allocated+size > ba.limit {
		return nil, fmt.Errorf("%w: need %d bytes, used %d / %d",
			ErrMemoryLimitExceeded, size, ba.allocated, ba.limit)
	}
	ba.allocated += size
	return Alloc[T](ba.inner), nil
}

// BoundedAllocSlice allocates a []T slice via the BoundedAllocator.
// Returns ErrMemoryLimitExceeded if the ceiling would be breached.
// Thread-safe for concurrent use.
func BoundedAllocSlice[T any](ba *BoundedAllocator, n int) ([]T, error) {
	ba.mu.Lock()
	defer ba.mu.Unlock()

	var zero T
	size := int64(unsafe.Sizeof(zero)) * int64(n)
	if ba.allocated+size > ba.limit {
		return nil, fmt.Errorf("%w: need %d bytes, used %d / %d",
			ErrMemoryLimitExceeded, size, ba.allocated, ba.limit)
	}
	ba.allocated += size
	return AllocSlice[T](ba.inner, n), nil
}

// AllocatorPool manages a pool of Allocators for concurrent use across multiple goroutines.
type AllocatorPool struct {
	p sync.Pool
}

// NewPool creates an AllocatorPool
func NewPool(slabSize int) *AllocatorPool {
	ap := &AllocatorPool{}
	ap.p.New = func() any { return NewSize(slabSize) }
	return ap
}

// Get an Allocator from the pool
func (ap *AllocatorPool) Get() *Allocator { return ap.p.Get().(*Allocator) }

// Return resets the Allocator and returns it to the pool for reuse.
func (ap *AllocatorPool) Return(a *Allocator) {
	a.Reset()
	ap.p.Put(a)
}

type slabPool struct {
	mu     sync.Mutex
	bySize map[int][]*slab
}

var globalPool = &slabPool{bySize: make(map[int][]*slab)}

func (p *slabPool) get(size int) *slab{
	p.mu.Lock()
	list := p.bySize[size]
	if len(list) == 0 {
		p.mu.Unlock()
		return nil
	}
	s := list[len(list)-1]
	p.bySize[size] = list[:len(list)-1]
	p.mu.Unlock()
	s.reset()
	return s
}

func (p *slabPool) put(s *slab) {
	size := cap(s.buf)
	p.mu.Lock()
	p.bySize[size] = append(p.bySize[size], s)
	p.mu.Unlock()
}

func nextPow2(n int) int {
	if n <= 1 {
		return 1
	}
	return 1 << bits.Len(uint(n-1))
}

func clampSlabSize(size int) int {
	if size < 64 {
		size = 64 // for alignment
	}
	size = nextPow2(size)
	if size > MaxSlabSize {
		size = MaxSlabSize
	}
	return size
}
