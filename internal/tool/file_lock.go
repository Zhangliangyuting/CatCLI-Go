package tool

import (
	"path/filepath"
	"sync"
)

// pathLockManager coordinates file access inside this process. Different paths
// use different locks, so unrelated files can still be processed concurrently.
type pathLockManager struct {
	mu    sync.Mutex
	locks map[string]*sync.RWMutex
}

func newPathLockManager() *pathLockManager {
	return &pathLockManager{locks: make(map[string]*sync.RWMutex)}
}

func (m *pathLockManager) lock(path string) func() {
	lock := m.pathLock(path)
	lock.Lock()
	return lock.Unlock
}

func (m *pathLockManager) rlock(path string) func() {
	lock := m.pathLock(path)
	lock.RLock()
	return lock.RUnlock
}

func (m *pathLockManager) pathLock(path string) *sync.RWMutex {
	key := canonicalFilePath(path)

	m.mu.Lock()
	defer m.mu.Unlock()

	lock, exists := m.locks[key]
	if !exists {
		lock = &sync.RWMutex{}
		m.locks[key] = lock
	}
	return lock
}

func canonicalFilePath(path string) string {
	cleanPath := filepath.Clean(path)
	absPath, err := filepath.Abs(cleanPath)
	if err == nil {
		cleanPath = absPath
	}
	if resolvedPath, err := filepath.EvalSymlinks(cleanPath); err == nil {
		return resolvedPath
	}

	// The target may not exist yet (write_file/create_project). Resolve its
	// existing parent so a symlinked directory cannot produce a second lock key.
	parent, name := filepath.Split(cleanPath)
	if resolvedParent, err := filepath.EvalSymlinks(parent); err == nil {
		cleanPath = filepath.Join(resolvedParent, name)
	}
	return cleanPath
}

var fileLocks = newPathLockManager()
