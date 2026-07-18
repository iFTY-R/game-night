//go:build !windows

package local

import "os"

// syncDirectory makes link and unlink directory entries durable. Open, sync,
// and close failures are all propagated because they may indicate lost durability.
func syncDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
