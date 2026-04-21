//go:build windows

package cmd

import (
	"fmt"
	"os"
)

func openExistingCreateAPIKeyFile(file string) (*os.File, error) {
	// Go does not expose a no-follow open on Windows, so this is a best-effort
	// symlink rejection before opening the existing file for reuse.
	info, err := os.Lstat(file)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing to write secret to symlink %q", file)
	}
	return os.OpenFile(file, os.O_WRONLY, 0)
}
