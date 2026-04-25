//go:build unix

// The unix build constraint is a standard Go tag (Go 1.19+) for Unix-like GOOS values.

package cmd

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func openExistingCreateAPIKeyFile(file string) (*os.File, error) {
	// Keep the low-level open here: we need no-follow plus non-blocking and
	// close-on-exec behavior before we can stat/reject the opened descriptor.
	// os.OpenFile with os.O_NOFOLLOW cannot express that full combination.
	fd, err := syscall.Open(file, syscall.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("refusing to write secret to symlink %q", file)
		}
		return nil, err
	}
	return os.NewFile(uintptr(fd), file), nil
}
