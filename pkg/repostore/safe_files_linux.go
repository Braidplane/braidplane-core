//go:build linux

package repostore

import "golang.org/x/sys/unix"

// openNoSymlinkAt uses openat2 to keep Linux path resolution beneath one directory descriptor.
func openNoSymlinkAt(directoryFD int, name string, flags int, mode uint32) (int, error) {
	if flags < 0 {
		return -1, ErrRepositorySetup.Detail("invalid file open flags")
	}

	fd, err := unix.Openat2(directoryFD, name, &unix.OpenHow{
		Flags: uint64(flags) | uint64(unix.O_CLOEXEC) | uint64(unix.O_NOFOLLOW),
		Mode:  uint64(mode),
		Resolve: unix.RESOLVE_BENEATH |
			unix.RESOLVE_NO_SYMLINKS |
			unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return -1, ErrRepositorySetup.With(err)
	}
	return fd, nil
}
