// Copyright 2013 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sync

import (
	"internal/race"
	"runtime"
	"sync/atomic"
	"unsafe"
)
// https://www.kancloud.cn/mutouzhang/go/596830
// https://www.jianshu.com/p/2e08332481c5
// https://www.kancloud.cn/mutouzhang/go/596830

// 多核 cpu 高并发编程，就是要每个 cpu 拥有自己的本地数据，这样就避免了锁争用的开销。而事实上 sync.Pool 也是这么做的。

// A Pool is a set of temporary objects that may be individually saved and retrieved.
// Pool 是一个可以进行存取操作的临时对象集合

// Any item stored in the Pool may be removed automatically at any time without notification.
// Pool 中的对象会在任意时刻被自动移除并且没有任何通知.

// If the Pool holds the only reference when this happens, the item might be deallocated.
// 如果在对象被移除 Pool 的时候,Pool 持有的是该对象仅有的应用,那么这个对象可能被gc回收.

// A Pool is safe for use by multiple goroutines simultaneously.
// Pool 在多 goroutine 环境中是安全的.

// Pool's purpose is to cache allocated but unused items for later reuse, relieving pressure on the garbage collector.
// Pool 的作用是缓存已经被分配但是还没有使用的对象,以便之后使用,以此缓解 gc 的压力

// That is, it makes it easy to build efficient, thread-safe free lists. However, it is not suitable for all free lists.
// Pool 可以是建立一个高效的,线程安全的 free list （一种用于动态内存申请的数据结构） 更容易,但是它并不是适用于所有的 free list

// An appropriate use of a Pool is to manage a group of temporary items silently shared among and potentially reused by concurrent independent clients of a package.
// Pool 的一个使用场景是, 去管理在 package 被多个独立线程共享的一组临时对象.

// Pool provides a way to amortize allocation overhead across many clients.
// Pool 提供了一种在多线程间缓冲内存分配开销的方法.

// An example of good use of a Pool is in the fmt package, which maintains a dynamically-sized store of temporary output buffers.
// Pool 的一个很好的使用案例是在 fmt 包中, Pool 维持了一个临时的动态大小的输出buffers

// The store scales under load (when many goroutines are actively printing) and shrinks when quiescent.

// On the other hand, a free list maintained as part of a short-lived object is not a suitable use for a Pool, since the overhead does not amortize well in that scenario.
// 另外，一些短生命周期的对象不适合使用 pool 来维护，因为在那种情况下不能很好的缓冲开销.

// It is more efficient to have such objects implement their own free list.
// 应该使用它们自己的 free list（这里可能指的是 go 内存模型中用于缓存 <32k小对象的 free list） 更高效。

// A Pool must not be copied after first use.
// Pool 一旦使用，不能被复制。
type Pool struct {
	noCopy noCopy 	// noCopy 字段，Pool 默认创建后禁止拷贝，必须使用指针。noCopy 用来编绎时 go vet 检查，静态语言就是爽，编绎期干了好多脏活累活

	local     unsafe.Pointer // local fixed-size per-P pool, actual type is [P]poolLocal 本地池指针 为每个thread 维护了一个poolLocal 数据结构。不同线程取数据的时候，先判断下hash 到哪个线程去了，分别去对应的poolLocal 中去取数据，这是利用了分段锁的思想。
	localSize uintptr        // size of the local array 池大小

	victim     unsafe.Pointer // local from previous cycle . victim cache, 以减少 GC 后冷启动导致的性能抖动, 对象至少存活两个 GC 区间
	victimSize uintptr        // size of victims array

	// New optionally specifies a function to generate
	// a value when Get would otherwise return nil.
	// It may not be changed concurrently with calls to Get.
	New func() interface{}
}

// Local per-P Pool appendix.
type poolLocalInternal struct {
	private interface{} // Can be used only by the respective P.
	shared  poolChain   // Local P can pushHead/popHead; any P can popTail. 专为 Pool 而设计，单生产者多消费者，多消费者消费时使用 CAS 实现无锁
}

type poolLocal struct {
	poolLocalInternal

	// Prevents false sharing on widespread platforms with
	// 128 mod (cache line size) = 0 .
	pad [128 - unsafe.Sizeof(poolLocalInternal{})%128]byte
}

// from runtime
func fastrand() uint32

var poolRaceHash [128]uint64

// poolRaceAddr returns an address to use as the synchronization point
// for race detector logic. We don't use the actual pointer stored in x
// directly, for fear of conflicting with other synchronization on that address.
// Instead, we hash the pointer to get an index into poolRaceHash.
// See discussion on golang.org/cl/31589.
func poolRaceAddr(x interface{}) unsafe.Pointer {
	ptr := uintptr((*[2]unsafe.Pointer)(unsafe.Pointer(&x))[1])
	h := uint32((uint64(uint32(ptr)) * 0x85ebca6b) >> 16)
	return unsafe.Pointer(&poolRaceHash[h%uint32(len(poolRaceHash))])
}

// Put adds x to the pool.
// Put 优先把元素放在 private 池中；如果 private 不为空，则放在 shared 池中。在入池之前，该元素有 1/4 可能被丢掉。
func (p *Pool) Put(x interface{}) {
	if x == nil {
		return
	}
	// 如果存在竞争
	if race.Enabled {
		if fastrand()%4 == 0 {
			// Randomly drop x on floor.
			return
		}
		race.ReleaseMerge(poolRaceAddr(x))
		race.Disable()
	}
	l, _ := p.pin()
	if l.private == nil {
		l.private = x
		x = nil
	}
	if x != nil {
		l.shared.pushHead(x)
	}
	runtime_procUnpin()
	if race.Enabled {
		race.Enable()
	}
}

// Get selects an arbitrary item from the Pool, removes it from the Pool, and returns it to the caller.
// Get 从 Pool 中随机选择一个元素返回给调用者,并将这个元素移出 Pool

// Get may choose to ignore the pool and treat it as empty.
// Get 可能选取忽略 Pool 并将它当成空的.

// Callers should not assume any relation between values passed to Put and the values returned by Get.
// 调用者不能假设任何出入Put和从Get中返回的值之间有任何联系.

// If Get would otherwise return nil and p.New is non-nil, Get returns the result of calling p.New.
// 如果 Get 返回 nil, 并且 p.New 不为 nil, Get 返回 p.New 的结果

// Pool 维护一个本地池，分为 私有池 private 和共享池 shared。私有池中的元素只能本地 Pool 使用，共享池中的元素可能会被其他 Pool 偷走，所以使用私有池 private 时不用加锁，而使用共享池 shared 时需加锁。
// Get 会优先查找本地 private，再查找本地 shared，最后查找其他 Pool 的 shared，如果以上全部没有可用元素，最后会调用 New 函数获取新元素。
func (p *Pool) Get() interface{} {
	if race.Enabled {
		race.Disable()   // race 检测时禁用 Pool 功能
	}
	// 获取本地 Pool 的 poolLocal 对象
	l, pid := p.pin()
	// 先获取 private 池中的对象（只有一个）
	x := l.private
	l.private = nil
	if x == nil {
		// Try to pop the head of the local shard. We prefer
		// the head over the tail for temporal locality of
		// reuse.
		//
		x, _ = l.shared.popHead()
		if x == nil {
			// 查找其他 P 的 shared 池
			x = p.getSlow(pid)
		}
	}
	runtime_procUnpin()
	if race.Enabled {
		race.Enable()
		if x != nil {
			race.Acquire(poolRaceAddr(x))
		}
	}
	// 未找到可用元素，调用 New 生成
	if x == nil && p.New != nil {
		x = p.New()
	}
	return x
}

func (p *Pool) getSlow(pid int) interface{} {
	// See the comment in pin regarding ordering of the loads.
	size := atomic.LoadUintptr(&p.localSize) // load-acquire
	locals := p.local                        // load-consume
	// Try to steal one element from other procs.
	for i := 0; i < int(size); i++ {
		l := indexLocal(locals, (pid+i+1)%int(size))
		if x, _ := l.shared.popTail(); x != nil {
			return x
		}
	}

	// Try the victim cache. We do this after attempting to steal
	// from all primary caches because we want objects in the
	// victim cache to age out if at all possible.
	size = atomic.LoadUintptr(&p.victimSize)
	if uintptr(pid) >= size {
		return nil
	}
	locals = p.victim
	l := indexLocal(locals, pid)
	if x := l.private; x != nil {
		l.private = nil
		return x
	}
	for i := 0; i < int(size); i++ {
		l := indexLocal(locals, (pid+i)%int(size))
		if x, _ := l.shared.popTail(); x != nil {
			return x
		}
	}

	// Mark the victim cache as empty for future gets don't bother
	// with it.
	atomic.StoreUintptr(&p.victimSize, 0)

	return nil
}

// pin pins the current goroutine to P, disables preemption and
// returns poolLocal pool for the P and the P's id.
// Caller must call runtime_procUnpin() when done with the pool.
func (p *Pool) pin() (*poolLocal, int) {
	// 返回当前 P.id
	pid := runtime_procPin()
	// In pinSlow we store to local and then to localSize, here we load in opposite order.
	// Since we've disabled preemption, GC cannot happen in between.
	// Thus here we must observe local at least as large localSize.
	// We can observe a newer/larger local, it is fine (we must observe its zero-initialized-ness).
	s := atomic.LoadUintptr(&p.localSize) // load-acquire
	l := p.local                          // load-consume
	// 如果 P.id 没有超出数组索引限制，则直接返回
	// 这是考虑到 procresize/GOMAXPROCS 的影响
	if uintptr(pid) < s {
		return indexLocal(l, pid), pid
	}
	// 没有结果时，会涉及全局加锁操作
	// 比如重新分配数组内存，添加到全局列表
	return p.pinSlow()
}

func (p *Pool) pinSlow() (*poolLocal, int) {
	// Retry under the mutex.
	// Can not lock the mutex while pinned.
	runtime_procUnpin()
	allPoolsMu.Lock()
	defer allPoolsMu.Unlock()
	pid := runtime_procPin()
	// poolCleanup won't be called while we are pinned.
	// 再次检查是否符合条件，可能中途已被其他线程调用
	s := p.localSize
	l := p.local
	if uintptr(pid) < s {
		return indexLocal(l, pid), pid
	}
	// 如果数组为空，新建
	// 将其添加到 allPools，垃圾回收器以此获取所有 Pool 实例
	if p.local == nil {
		allPools = append(allPools, p)
	}
	// If GOMAXPROCS changes between GCs, we re-allocate the array and lose the old one.
	size := runtime.GOMAXPROCS(0)
	local := make([]poolLocal, size)
	atomic.StorePointer(&p.local, unsafe.Pointer(&local[0])) // store-release
	atomic.StoreUintptr(&p.localSize, uintptr(size))         // store-release
	return &local[pid], pid
}

//如果 GC 发生时，某个 goroutine 正在访问 l.shared，整个 Pool 将会保留，下次执行时将会有双倍内存
func poolCleanup() {
	// This function is called with the world stopped, at the beginning of a garbage collection.
	// 当世界暂停，垃圾回收将要开始时， poolCleanup 会被调用。

	// It must not allocate and probably should not call any runtime functions.
	// 该函数内不能分配内存且不能调用任何运行时函数。防止错误的保留整个 Pool

	// Because the world is stopped, no pool user can be in a
	// pinned section (in effect, this has all Ps pinned).

	// Drop victim caches from all pools.
	for _, p := range oldPools {
		p.victim = nil
		p.victimSize = 0
	}

	// Move primary cache to victim cache.
	for _, p := range allPools {
		p.victim = p.local
		p.victimSize = p.localSize
		p.local = nil
		p.localSize = 0
	}

	// The pools with non-empty primary caches now have non-empty
	// victim caches and no pools have primary caches.
	oldPools, allPools = allPools, nil
}

var (
	allPoolsMu Mutex

	// allPools is the set of pools that have non-empty primary
	// caches. Protected by either 1) allPoolsMu and pinning or 2)
	// STW.
	allPools []*Pool

	// oldPools is the set of pools that may have non-empty victim
	// caches. Protected by STW.
	oldPools []*Pool
)

func init() {
	runtime_registerPoolCleanup(poolCleanup)
}

func indexLocal(l unsafe.Pointer, i int) *poolLocal {
	lp := unsafe.Pointer(uintptr(l) + uintptr(i)*unsafe.Sizeof(poolLocal{}))
	return (*poolLocal)(lp)
}

// Implemented in runtime.
func runtime_registerPoolCleanup(cleanup func())
func runtime_procPin() int
func runtime_procUnpin()
