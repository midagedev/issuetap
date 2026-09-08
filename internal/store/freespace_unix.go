//go:build !windows

package store

import (
	"fmt"
	"syscall"
)

// requireFreeSpace refuses with the number when the filesystem holding
// path cannot take need more bytes. A migration that dies on ENOSPC
// halfway is recoverable but frightening; this is the cheap version of
// not frightening anyone.
func requireFreeSpace(path string, need int64) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dirOf(path), &st); err != nil {
		return nil // cannot tell — proceed rather than block on a guess
	}
	free := int64(st.Bavail) * int64(st.Bsize)
	if free < need {
		return fmt.Errorf("needs about %d MiB free, has %d MiB", need>>20, free>>20)
	}
	return nil
}
