// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sync

import (
	"sync/atomic"
	"unsafe"
)
/**
https://my.oschina.net/u/168737/blog/1542232
https://www.sunliver.com/2018/12/10/sync.map/ 案例


 */

/**
刚初始化时，read 和 dirty 都为空
刚开始插入数据时，read 为空，dirty 不为空
不断 load，当 misses 数达到阈值时，flush dirty 到 read。此时 read 不为空，dirty 为空
继续插入数据，此时 read 和 dirty 都不为空

 */

// Map is like a Go map[interface{}]interface{} but is safe for concurrent use
// by multiple goroutines without additional locking or coordination.
// Loads, stores, and deletes run in amortized constant time.

// sync.Map 与 go 内置的 map 类似,不同的是 sync.Map 用于并发安全的多线程编程,且不需要加锁或协调.
// 存取和删除操作以分摊的常量时间运行~?.

// The Map type is specialized. Most code should use a plain Go map instead,
// with separate locking or coordination, for better type safety and to make it
// easier to maintain other invariants along with the map content.

// sync.Map 是专为并发安全设计的.~?

// The Map type is optimized for two common use cases: (1) when the entry for a given
// key is only ever written once but read many times, as in caches that only grow,
// or (2) when multiple goroutines read, write, and overwrite entries for disjoint
// sets of keys. In these two cases, use of a Map may significantly reduce lock
// contention compared to a Go map paired with a separate Mutex or RWMutex.

// *** 重要 ***
// sync.Map 针对一下两种场景进行了优化:
// (1) 当存储的值像只会增加的缓存一样,单词写多次读
// (2) 当多个 goroutine 对不想交的 key 集合进行读写和覆盖操作.
// 在这两种场景下与普通 map 使用额外的锁不同, sync.Map 可以显著减少由锁争夺引起的资源消耗

// *!* 注意, 使用 sync.Map 有一个很重要的前提, 多个 goroutine 操作的key的集合互不相交

// sync.Map "拆箱可用", 第一次使用后不可以复制.
// The zero Map is empty and ready for use. A Map must not be copied after first use.
type Map struct {
	// 独占锁,在对 dirty 数据进行操作时使用
	mu Mutex

	// read contains the portion of the map's contents that are safe for
	// concurrent access (with or without mu held).

	// read 包含map的一部分数据,它们通过持有或不持有 mu 来保证并发安全.

	// The read field itself is always safe to load, but must only be stored with
	// mu held.

	// read 字段对于读操作总是安全的,但只能在持有 mu 的时候进行写操作
	//

	// Entries stored in read may be updated concurrently without mu, but updating
	// a previously-expunged entry requires that the entry be copied to the dirty
	// map and unexpunged with mu held.

	// 保存在 map 中的 key-val 可能会被并发的更新,在更新已经被删除的 key-val 是要求 key-val 被复制到 dirty-map 并且在持有 mu 的时候恢复被删除的 key-val
	read atomic.Value // readOnly

	// dirty contains the portion of the map's contents that require mu to be
	// held. To ensure that the dirty map can be promoted to the read map quickly,
	// it also includes all of the non-expunged entries in the read map.

	// dirty 包含 map 中需要持有 mu 的那部分 key-val,为了确保将 dirty map 快速的提升为 read map, 它也包含 read map 中所有没有被删除的 key-val

	// Expunged entries are not stored in the dirty map. An expunged entry in the
	// clean map must be unexpunged and added to the dirty map before a new value
	// can be stored to it.

	// 被删除的 key-val 不存储在 dirty-map. 在 clean-map 中删除一个 key-val, 这个key-val 必须是未被删除的,并且要在存储新值前添加进 dirty-map

	// If the dirty map is nil, the next write to the map will initialize it by
	// making a shallow copy of the clean map, omitting stale entries.

	// 如果 dirty-map 是 nil, 下一次对 map 进行写操作时会通过浅拷贝clean-map 对 dirty-map 进行初始化,忽略就的 key-val
	dirty map[interface{}]*entry

	// misses counts the number of loads since the read map was last updated that
	// needed to lock mu to determine whether the key was present.

	// missed 记录 read-map 最后一次更新后的读取操作次数,

	//
	// Once enough misses have occurred to cover the cost of copying the dirty
	// map, the dirty map will be promoted to the read map (in the unamended
	// state) and the next store to the map will make a new dirty copy.
	misses int
}

// readOnly is an immutable struct stored atomically in the Map.read field.
type readOnly struct {
	m       map[interface{}]*entry
	// 标记 dirty-map 是否有 read 中没有的 key-val
	amended bool // true if the dirty map contains some key not in m.
}

// expunged is an arbitrary pointer that marks entries which have been deleted
// from the dirty map.
var expunged = unsafe.Pointer(new(interface{}))

// An entry is a slot in the map corresponding to a particular key.
type entry struct {
	//nil: entry已被删除了，并且m.dirty为nil
	//expunged: entry已被删除了，并且m.dirty不为nil，而且这个entry不存在于m.dirty中
	//其它： entry是一个正常的值

	// p points to the interface{} value stored for the entry.
	//
	// If p == nil, the entry has been deleted and m.dirty == nil.
	//
	// If p == expunged, the entry has been deleted, m.dirty != nil, and the entry
	// is missing from m.dirty.
	//
	// Otherwise, the entry is valid and recorded in m.read.m[key] and, if m.dirty
	// != nil, in m.dirty[key].
	//
	// An entry can be deleted by atomic replacement with nil: when m.dirty is
	// next created, it will atomically replace nil with expunged and leave
	// m.dirty[key] unset.
	//
	// An entry's associated value can be updated by atomic replacement, provided
	// p != expunged. If p == expunged, an entry's associated value can be updated
	// only after first setting m.dirty[key] = e so that lookups using the dirty
	// map find the entry.
	p unsafe.Pointer // *interface{}
}

func newEntry(i interface{}) *entry {
	return &entry{p: unsafe.Pointer(&i)}
}

// Load returns the value stored in the map for a key, or nil if no
// value is present.
// The ok result indicates whether value was found in the map.
func (m *Map) Load(key interface{}) (value interface{}, ok bool) {
	// 从 read 中读,不需要加锁
	read, _ := m.read.Load().(readOnly)
	e, ok := read.m[key]
	// 如果在 read 中没有找到对应key的值,且 dirty-map 中有 read 中没有的 key-val,则尝试在 dirty-map 中取值
	if !ok && read.amended {
		m.mu.Lock()
		// Avoid reporting a spurious miss if m.dirty got promoted while we were
		// blocked on m.mu. (If further loads of the same key will not miss, it's
		// not worth copying the dirty map for this key.)

		// 双检查，避免返回伪未命中, 加锁阻塞的时候 m.dirty 可能会被提升为 m.read,这个时候 m.read 可能被替换了。
		// 如果可以从 read 中读到这个 key, 就不需要为了这个 key 复制 dirty-map

		read, _ = m.read.Load().(readOnly)
		e, ok = read.m[key]
		if !ok && read.amended {
			e, ok = m.dirty[key]
			// Regardless of whether the entry was present, record a miss: this key
			// will take the slow path until the dirty map is promoted to the read map.

			// 不管这个 key 是否存在,记录 miss 次数: 这个 key 的读取将使用这种慢速的方式,直到 dirty-map 被提升为 read-map

			// 增加 misses 次数, 当 miss >= dirty-map 的长度时,将 dirty-map 复制为 read-map,并将 misses 设置为 0
			m.missLocked()
		}
		m.mu.Unlock()
	}
	if !ok {
		return nil, false
	}
	return e.load()
}

func (e *entry) load() (value interface{}, ok bool) {
	p := atomic.LoadPointer(&e.p)
	// nil: entry不存在，并且 m.dirty 为 nil
	// expunged: entry已被删除了，并且 m.dirty 不为 nil，而且这个entry不存在于 m.dirty 中
	if p == nil || p == expunged {
		return nil, false
	}
	return *(*interface{})(p), true
}

/**
如果 read 中存在且没有被删除(没有被标记为 expunge)，则直接使用 CAS 进行更新
如果 read 中存在且被删除了，则将其存在 dirty 中，原因在 Delete 函数的说明中
如果 dirty 中存在，则将其存在 dirty 中
这时候，如果 read 中的 amended 标记为 false, 则需要重新构建 dirty map，将 read 中的数据拷贝至 dirty，然后将数据存在 dirty 中
dirtyLocked 函数中负责将 read 中没有标记为 expunge 的值拷贝到 dirty 中。在这里会将 nil 改成 expunge

 */
// Store sets the value for a key.
// 存储 key 对应的 value 集合
func (m *Map) Store(key, value interface{}) {
	read, _ := m.read.Load().(readOnly)
	// 如果key在read-map中,则直接存,因为m.dirty也指向这个entry,所以m.dirty也保持最新的entry。
	if e, ok := read.m[key]; ok && e.tryStore(&value) {
		return
	}

	// 获取锁
	m.mu.Lock()
	read, _ = m.read.Load().(readOnly)

	if e, ok := read.m[key]; ok {
		// read 中存在,但被标记为了已删除;
		if e.unexpungeLocked() {
			// 将 e.p 置为 nil, 表示entry 不存在;在这个,一个被删除的元素才真正被从 read 中删除

			// The entry was previously expunged, which implies that there is a non-nil dirty map and this entry is not in it.

			// key-val 在之前被删除过,这意味着 dirty-map 不是 nil,并且这个 key-val 不在其中
			m.dirty[key] = e
		}
		e.storeLocked(&value)
	} else if e, ok := m.dirty[key]; ok {
		// 在 dirty-map 中存在
		e.storeLocked(&value)
	} else {
		// 既不在 read-map 中. 也不在 dirty-map 中
		if !read.amended {
			// We're adding the first new key to the dirty map.
			// Make sure it is allocated and mark the read-only map as incomplete.
			// 给 dirty-map 添加第一个值,要注意把 readOnly 的 amended 设置为true, 表示 dirty-map 中存在与 read-map 中没有的 entry
			m.dirtyLocked()  // 初始化 dirty-map
			m.read.Store(readOnly{m: read.m, amended: true})
		}
		m.dirty[key] = newEntry(value)
	}
	m.mu.Unlock()
}

// tryStore stores a value if the entry has not been expunged.

// tryStore 存储没有被删除的 key-val

// If the entry is expunged, tryStore returns false and leaves the entry
// unchanged.

// 如果这个 key-val 已经被删除, tryStore 返回 false,并保持 entry 不变.
func (e *entry) tryStore(i *interface{}) bool {
	// 考虑到并发安全,更新 value 需要使用 cas 模式,所以使用 for 循环,直到value被设置成功或key被删除为止
	for {
		p := atomic.LoadPointer(&e.p)
		// key-val 已被删除
		if p == expunged {
			return false
		}
		// 使用 cas 对 value 进行更新
		if atomic.CompareAndSwapPointer(&e.p, p, unsafe.Pointer(i)) {
			return true
		}
	}
}

// unexpungeLocked ensures that the entry is not marked as expunged.
//
// If the entry was previously expunged, it must be added to the dirty map
// before m.mu is unlocked.
func (e *entry) unexpungeLocked() (wasExpunged bool) {
	return atomic.CompareAndSwapPointer(&e.p, expunged, nil)
}

// storeLocked unconditionally stores a value to the entry. The entry must be known not to be expunged.
// 无条件的将 value 存入 entry, 这个 entry 必须是没有被删除的.
func (e *entry) storeLocked(i *interface{}) {
	atomic.StorePointer(&e.p, unsafe.Pointer(i))
}

// LoadOrStore returns the existing value for the key if present.
// Otherwise, it stores and returns the given value.
// The loaded result is true if the value was loaded, false if stored.

// 如果 key-val对存在, actual 返回该 val, loaded 返回 true;否则,存储传入的 val, loaded 返回 false.
func (m *Map) LoadOrStore(key, value interface{}) (actual interface{}, loaded bool) {
	// Avoid locking if it's a clean hit.
	// 如果key在 read.map 存在并且未被删除,则可以快速读取返回,不需要加锁.
	read, _ := m.read.Load().(readOnly)
	if e, ok := read.m[key]; ok {
		actual, loaded, ok := e.tryLoadOrStore(value)
		if ok {
			return actual, loaded
		}
	}

	m.mu.Lock()
	read, _ = m.read.Load().(readOnly)
	if e, ok := read.m[key]; ok {
		if e.unexpungeLocked() {
			m.dirty[key] = e
		}
		actual, loaded, _ = e.tryLoadOrStore(value)
	} else if e, ok := m.dirty[key]; ok {
		actual, loaded, _ = e.tryLoadOrStore(value)
		m.missLocked()
	} else {
		if !read.amended {
			// We're adding the first new key to the dirty map.
			// Make sure it is allocated and mark the read-only map as incomplete.
			m.dirtyLocked()
			m.read.Store(readOnly{m: read.m, amended: true})
		}
		m.dirty[key] = newEntry(value)
		actual, loaded = value, false
	}
	m.mu.Unlock()

	return actual, loaded
}

// tryLoadOrStore atomically loads or stores a value if the entry is not expunged.

// 如果 key-val 未被删除, tryLoadOrStore 可以原子的存取 value

// If the entry is expunged, tryLoadOrStore leaves the entry unchanged and returns with ok==false.
// 如果 key-val 已被删除, tryLoadOrStore 保持 entry 不变,并且 返回 ok==false
func (e *entry) tryLoadOrStore(i interface{}) (actual interface{}, loaded, ok bool) {
	p := atomic.LoadPointer(&e.p)
	if p == expunged {
		return nil, false, false
	}
	if p != nil {
		return *(*interface{})(p), true, true
	}

	// Copy the interface after the first load to make this method more amenable
	// to escape analysis: if we hit the "load" path or the entry is expunged, we
	// shouldn't bother heap-allocating.
	ic := i
	for {
		if atomic.CompareAndSwapPointer(&e.p, nil, unsafe.Pointer(&ic)) {
			return i, false, true
		}
		p = atomic.LoadPointer(&e.p)
		if p == expunged {
			return nil, false, false
		}
		if p != nil {
			return *(*interface{})(p), true, true
		}
	}
}

/**
read 中找到了则将其改成 nil
dirty 中找到了则直接删除

 */
// Delete deletes the value for a key.
func (m *Map) Delete(key interface{}) {
	read, _ := m.read.Load().(readOnly)
	e, ok := read.m[key]
	if !ok && read.amended {
		m.mu.Lock()
		read, _ = m.read.Load().(readOnly)
		e, ok = read.m[key]
		if !ok && read.amended {
			delete(m.dirty, key)
		}
		m.mu.Unlock()
	}
	if ok {
		e.delete()
	}
}

func (e *entry) delete() (hadValue bool) {
	for {
		p := atomic.LoadPointer(&e.p)
		if p == nil || p == expunged {
			return false
		}
		if atomic.CompareAndSwapPointer(&e.p, p, nil) {
			return true
		}
	}
}

// Range calls f sequentially for each key and value present in the map.
// If f returns false, range stops the iteration.
//
// Range does not necessarily correspond to any consistent snapshot of the Map's
// contents: no key will be visited more than once, but if the value for any key
// is stored or deleted concurrently, Range may reflect any mapping for that key
// from any point during the Range call.
//
// Range may be O(N) with the number of elements in the map even if f returns
// false after a constant number of calls.
func (m *Map) Range(f func(key, value interface{}) bool) {
	// We need to be able to iterate over all of the keys that were already
	// present at the start of the call to Range.
	// If read.amended is false, then read.m satisfies that property without
	// requiring us to hold m.mu for a long time.
	read, _ := m.read.Load().(readOnly)
	if read.amended {
		// m.dirty contains keys not in read.m. Fortunately, Range is already O(N)
		// (assuming the caller does not break out early), so a call to Range
		// amortizes an entire copy of the map: we can promote the dirty copy
		// immediately!
		m.mu.Lock()
		read, _ = m.read.Load().(readOnly)
		if read.amended {
			read = readOnly{m: m.dirty}
			m.read.Store(read)
			m.dirty = nil
			m.misses = 0
		}
		m.mu.Unlock()
	}

	for k, e := range read.m {
		v, ok := e.load()
		if !ok {
			continue
		}
		if !f(k, v) {
			break
		}
	}
}

func (m *Map) missLocked() {
	m.misses++
	if m.misses < len(m.dirty) {
		return
	}
	m.read.Store(readOnly{m: m.dirty})
	m.dirty = nil
	m.misses = 0
}

// 初始化 dirty
func (m *Map) dirtyLocked() {
	if m.dirty != nil {
		return
	}

	read, _ := m.read.Load().(readOnly)
	m.dirty = make(map[interface{}]*entry, len(read.m))
	for k, e := range read.m {
		if !e.tryExpungeLocked() {
			// 将未被删除的 key-val 存入 dirty
			m.dirty[k] = e
		}
	}
}

func (e *entry) tryExpungeLocked() (isExpunged bool) {
	p := atomic.LoadPointer(&e.p)
	for p == nil {
		if atomic.CompareAndSwapPointer(&e.p, nil, expunged) {
			// 将已经删除标记为nil的数据标记为 expunged
			return true
		}
		p = atomic.LoadPointer(&e.p)
	}
	return p == expunged
}
