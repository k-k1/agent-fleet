//go:build !linux

package codex

import (
	"fmt"
	"os"
)

// statfsIsNFS answers false off Linux: the keep volume exists only on ecs-ec2, and the Agent
// itself is linux-only (fs_fd_linux.go).
func statfsIsNFS(string) bool { return false }

// fileID identifies one generation of a file by size and mtime (no inode here).
func fileID(fi os.FileInfo) string {
	return fmt.Sprintf("%d:%d", fi.Size(), fi.ModTime().UnixNano())
}
