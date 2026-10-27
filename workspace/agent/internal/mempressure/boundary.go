package mempressure

import (
	"path/filepath"
	"strings"
)

// Boundary is the answer to "is the cgroup directory we read this container's own?".
type Boundary struct {
	OK     bool
	Reason string // why not, when !OK
}

// CheckBoundary is ADR 0120 decision 1's "verified means resolved, not merely readable". A
// readable memory.pressure proves nothing about whose it is: with a host cgroup namespace
// /sys/fs/cgroup is the host's root cgroup, and on a machine that gave the Agent no cgroup of
// its own it is someone else's. The directory is accepted only if the cgroup it resolves to is
// the one /proc/self/cgroup names for this process, and the only way that holds with a root
// mount ("0::/") is a private cgroup namespace.
//
// selfCgroup is /proc/self/cgroup, mountinfo is /proc/self/mountinfo, dir is the directory the
// sampler reads. When in doubt the answer is !OK: the cost of a false "not verified" is a missing
// axis, the cost of a false "verified" is a number about somebody else's memory.
func CheckBoundary(dir, selfCgroup, mountinfo string) Boundary {
	own, ok := unifiedPath(selfCgroup)
	if !ok {
		return Boundary{Reason: "no cgroup v2 entry in /proc/self/cgroup (cgroup v1 or hybrid host)"}
	}
	root, mnt, ok := mountFor(filepath.Clean(dir), mountinfo)
	if !ok {
		return Boundary{Reason: "cgroup directory is not under a cgroup2 mount"}
	}
	rel, err := filepath.Rel(mnt, filepath.Clean(dir))
	if err != nil {
		return Boundary{Reason: "cgroup directory is not under its mount"}
	}
	resolved := filepath.Join("/", root, rel)
	if resolved != own {
		return Boundary{Reason: "cgroup directory resolves to " + resolved + ", not this process's cgroup " + own}
	}
	return Boundary{OK: true}
}

// unifiedPath returns the cgroup v2 path ("0::/path") from /proc/self/cgroup.
func unifiedPath(s string) (string, bool) {
	for _, line := range strings.Split(s, "\n") {
		if p, ok := strings.CutPrefix(line, "0::"); ok {
			return filepath.Join("/", strings.TrimSpace(p)), true
		}
	}
	return "", false
}

// mountFor finds the cgroup2 mount whose mount point is the longest prefix of dir and returns the
// mount's root (the path inside the cgroup hierarchy it shows) and its mount point.
func mountFor(dir, mountinfo string) (root, mnt string, ok bool) {
	for _, line := range strings.Split(mountinfo, "\n") {
		pre, post, found := strings.Cut(line, " - ")
		if !found {
			continue
		}
		if f := strings.Fields(post); len(f) == 0 || f[0] != "cgroup2" {
			continue
		}
		f := strings.Fields(pre)
		if len(f) < 5 {
			continue
		}
		r, m := unescapeMount(f[3]), unescapeMount(f[4])
		if dir != m && !strings.HasPrefix(dir, strings.TrimSuffix(m, "/")+"/") {
			continue
		}
		if !ok || len(m) > len(mnt) {
			root, mnt, ok = r, m, true
		}
	}
	return root, mnt, ok
}

// unescapeMount decodes mountinfo's octal escapes (\040 for a space and so on).
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, ok := octal3(s[i+1 : i+4]); ok {
				b.WriteByte(n)
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func octal3(s string) (byte, bool) {
	if len(s) != 3 {
		return 0, false
	}
	var n int
	for _, c := range []byte(s) {
		if c < '0' || c > '7' {
			return 0, false
		}
		n = n*8 + int(c-'0')
	}
	if n > 255 {
		return 0, false
	}
	return byte(n), true
}
