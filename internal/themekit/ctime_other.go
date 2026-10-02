//go:build !darwin && !linux

package themekit

import "io/fs"

// changeTime is not available here; size and modification time are used.
func changeTime(fs.FileInfo) int64 { return 0 }
