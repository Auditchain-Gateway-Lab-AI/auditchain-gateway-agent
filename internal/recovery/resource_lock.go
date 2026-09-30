package recovery

import "sync"

type ResourceLocker struct {
	mu    sync.Mutex
	locks map[string]*resourceLock
}

type resourceLock struct {
	refs int
	mu   sync.Mutex
}

func NewResourceLocker() *ResourceLocker {
	return &ResourceLocker{locks: make(map[string]*resourceLock)}
}

func (l *ResourceLocker) Lock(resource string) func() {
	l.mu.Lock()
	lock := l.locks[resource]
	if lock == nil {
		lock = &resourceLock{}
		l.locks[resource] = lock
	}
	lock.refs++
	l.mu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		l.mu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(l.locks, resource)
		}
		l.mu.Unlock()
	}
}
