//go:build !unix && !windows

package cmd

import (
	"fmt"
	"os"
)

func openExistingCreateAPIKeyFile(file string) (*os.File, error) {
	// Best-effort symlink rejection for platforms without a no-follow open API
	// in the Go standard library.
	info, err := os.Lstat(file)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing to write secret to symlink %q", file)
	}
	return os.OpenFile(file, os.O_WRONLY, 0)
}
