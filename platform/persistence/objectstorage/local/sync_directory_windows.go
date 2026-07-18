//go:build windows

package local

import "os"

// syncDirectory is intentionally a no-op on Windows. Go's portable os.Root API
// cannot open a directory with the write access FlushFileBuffers requires; file
// contents are still flushed before the atomic hard-link publication.
func syncDirectory(*os.Root) error {
	return nil
}
