//go:build !linux

package codex

// statfsIsNFS answers false off Linux: the keep volume exists only on ecs-ec2, and the Agent
// itself is linux-only (fs_fd_linux.go).
func statfsIsNFS(string) bool { return false }
