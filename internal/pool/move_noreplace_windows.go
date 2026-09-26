package pool

import "fmt"

// Recovery never moves untracked files on Windows.
func moveNoReplace(src, dst string) error {
	return fmt.Errorf("recovery backup moves are unsupported on Windows")
}
