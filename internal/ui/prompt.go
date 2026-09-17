package ui

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
)

var (
	stdinOnce   sync.Once
	stdinReader *bufio.Reader
)

// promptReader is one reader for the whole process. bufio pulls a whole chunk
// off the file descriptor, so a reader built per prompt discards every byte
// that arrived in the same read as the answer it consumed: piped answers after
// the first were lost and read back as EOF. A terminal hid it, because
// canonical mode delivers exactly one line per read.
func promptReader() *bufio.Reader {
	stdinOnce.Do(func() {
		stdinReader = bufio.NewReader(os.Stdin)
	})
	return stdinReader
}

func Confirm(message string, defaultYes bool) (bool, error) {
	hint := "Y/n"
	if !defaultYes {
		hint = "y/N"
	}

	fmt.Fprintf(os.Stderr, "%s [%s] ", message, hint)

	input, err := promptReader().ReadString('\n')
	if err != nil {
		return defaultYes, err
	}

	input = strings.TrimSpace(strings.ToLower(input))
	if input == "" {
		return defaultYes, nil
	}

	return input == "y" || input == "yes", nil
}
