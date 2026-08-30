package projcfg

import "os"

// WriteFileKeepMode writes b to path atomically (temp file + rename), preserving
// the file's EXISTING permission bits if it already exists, or creating it at
// 0644 if not. docs/log/56 §6: "モードは 0600 にしない…既存ファイルはモードを保ち、
// 新規作成は 0644" — unlike a user-scope config write, this file is checked out by
// other people and git only ever records 100644/100755, so 0600 could never
// actually be enforced here.
func WriteFileKeepMode(path string, b []byte) error {
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp := path + ".af-tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
