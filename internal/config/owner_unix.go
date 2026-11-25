//go:build unix

package config

import (
	"fmt"
	"os"
	"syscall"
)

// checkDiscoverable refuses a discovered config that someone other than the
// current user could have put there: a file owned by another user, a symbolic
// link, a file any user can write, or one sitting in a directory owned by
// another user or writable by every user (/tmp, a shared mount).
//
// Discovery walks up through parent directories, so without this a
// .wyrm.toml planted in /tmp ran its hooks for anyone typing `wyrm` anywhere
// beneath it. root-owned files are accepted: root can already do anything a
// config could. An explicit -config path skips this check — naming a file is
// the user vouching for it.
func checkDiscoverable(dir, file string) error {
	uid := int64(os.Getuid())
	fi, err := os.Lstat(file)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing %s: it is a symbolic link (pass -config to use it anyway)", file)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if int64(st.Uid) != uid && st.Uid != 0 {
		return fmt.Errorf("refusing %s: owned by uid %d, not you (pass -config to use it anyway)", file, st.Uid)
	}
	if fi.Mode().Perm()&0o002 != 0 {
		return fmt.Errorf("refusing %s: it is writable by every user (chmod o-w, or pass -config)", file)
	}
	di, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if dst, ok := di.Sys().(*syscall.Stat_t); ok {
		if int64(dst.Uid) != uid && dst.Uid != 0 {
			return fmt.Errorf("refusing %s: %s is owned by uid %d, not you or root (pass -config to use it anyway)", file, dir, dst.Uid)
		}
		if di.Mode().Perm()&0o002 != 0 && int64(dst.Uid) != uid {
			return fmt.Errorf("refusing %s: %s is writable by every user (pass -config to use it anyway)", file, dir)
		}
	}
	return nil
}
