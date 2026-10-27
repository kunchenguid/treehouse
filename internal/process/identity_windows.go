package process

// StartStamp is unsupported on Windows: a parent PID there is never reparented
// and can name an unrelated process once recycled, so a holder's ancestry, and
// with it the identity a lease records, cannot be established safely.
func StartStamp(pid int32) (int64, error) {
	return 0, ErrIdentityUnsupported
}
