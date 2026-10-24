//go:build linux

package egressagent

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"

	"golang.org/x/sys/unix"
)

// genRe is the shape of a generation id the agent writes.
var genRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// fileGen returns the log file's generation id, writing a new random one into the file's
// genXattr the first time the file is seen. An xattr stays with the file through a rename
// and is absent on a newly created file, which is exactly the identity the event ids
// need. Not being able to read or write it is an error, never a fallback: any identity
// taken from the file itself (inode, birth time, first line) can match a deleted file's.
func fileGen(f *os.File) (string, error) {
	fd := int(f.Fd())
	for attempt := 0; attempt < 2; attempt++ {
		buf := make([]byte, 64)
		n, err := unix.Fgetxattr(fd, genXattr, buf)
		if err == nil {
			if v := string(buf[:n]); genRe.MatchString(v) {
				return v, nil
			}
			return "", fmt.Errorf("%s holds %q, not a generation id", genXattr, buf[:n])
		}
		if !errors.Is(err, unix.ENODATA) {
			return "", fmt.Errorf("read %s: %w", genXattr, err)
		}
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		gen := hex.EncodeToString(b[:])
		err = unix.Fsetxattr(fd, genXattr, []byte(gen), unix.XATTR_CREATE)
		if err == nil {
			return gen, nil
		}
		if !errors.Is(err, unix.EEXIST) {
			return "", fmt.Errorf("set %s: %w", genXattr, err)
		}
		// Someone else tagged it between the read and the write: read theirs.
	}
	return "", fmt.Errorf("%s keeps changing", genXattr)
}
