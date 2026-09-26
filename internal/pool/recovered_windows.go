//go:build windows

package pool

import "errors"

// untrackedBackupUnsupported keeps a recovered Windows slot with untracked
// files leased: a backup folder there inherits its parent's ACL, so treehouse
// cannot keep it owner-only.
const untrackedBackupUnsupported = "untracked files are present, and moving them into a backup is not supported on Windows; preserve or remove them yourself"

func ensureOwnerOnlyDir(string) error {
	return errors.New("owner-only backup folders are not supported on Windows")
}
