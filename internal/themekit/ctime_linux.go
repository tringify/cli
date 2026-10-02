package themekit

import (
	"io/fs"
	"syscall"
)

// changeTime is the inode change time in nanoseconds, so a preview also
// notices edits that preserve the modification time.
func changeTime(info fs.FileInfo) int64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return st.Ctim.Nano()
	}
	return 0
}
