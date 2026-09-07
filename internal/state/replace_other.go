//go:build !windows

package state

import "os"

func replaceFile(from, to string) error { return os.Rename(from, to) }
