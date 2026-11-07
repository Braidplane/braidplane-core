//go:build darwin

package repostore

import "golang.org/x/sys/unix"

// openNoSymlinkAt walks one descriptor-pinned component without following a link on Darwin.
func openNoSymlinkAt(directoryFD int, name string, flags int, mode uint32) (int, error) {
	fd, err := unix.Openat(directoryFD, name, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW, mode)
	if err != nil {
		return -1, ErrRepositorySetup.With(err)
	}
	return fd, nil
}
