// Package maintenance provides a host-owned, crash-persistent admission fence.
package maintenance

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

var ErrActive = errors.New("installation maintenance active or unavailable")

// Enter holds a shared OS lock until the caller's mutation completes. The host
// takes the exclusive lock before writing active.json and checking final idle.
// Empty Directory is for deployment fixtures that do not enable host updates.
func Enter(directory string) (func(), error) {
	if directory == "" {
		return func() {}, nil
	}
	lock, err := os.Open(filepath.Join(directory, "admission.lock"))
	if err != nil {
		return nil, ErrActive
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, ErrActive
	}
	leave := func() { syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); lock.Close() }
	if _, err := os.Lstat(filepath.Join(directory, "active.json")); !os.IsNotExist(err) {
		leave()
		return nil, ErrActive
	}
	return leave, nil
}
