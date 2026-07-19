// Package secretfile securely reads one-time mounted plaintext secrets for non-HTTP processes.
package secretfile

import (
	"bytes"
	"errors"
	"io"
	"os"
	"runtime"
	"unicode/utf8"
)

const maximumBytes = 4 << 10

var ErrInvalid = errors.New("invalid secret file")

// Read accepts one read-only regular file and removes only its conventional final line ending.
func Read(path string) (string, bool, error) {
	if path == "" {
		return "", false, nil
	}
	info, err := os.Lstat(path)
	if err != nil || !secureMode(info.Mode()) {
		return "", false, ErrInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return "", false, ErrInvalid
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !secureMode(openedInfo.Mode()) || !os.SameFile(info, openedInfo) {
		return "", false, ErrInvalid
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximumBytes+1))
	if err != nil || len(contents) > maximumBytes {
		return "", false, ErrInvalid
	}
	contents = bytes.TrimSuffix(contents, []byte("\r\n"))
	contents = bytes.TrimSuffix(contents, []byte("\n"))
	if len(contents) == 0 || !utf8.Valid(contents) || bytes.ContainsAny(contents, "\x00\r\n") {
		return "", false, ErrInvalid
	}
	return string(contents), true, nil
}

func secureMode(mode os.FileMode) bool {
	if !mode.IsRegular() {
		return false
	}
	if runtime.GOOS == "windows" {
		return mode.Perm()&0o222 == 0
	}
	return mode.Perm() == 0o400
}
