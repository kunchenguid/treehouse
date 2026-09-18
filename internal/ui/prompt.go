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

	// ReadString reports io.EOF together with whatever it had already read, so
	// a final answer that arrives without a trailing newline comes back as a
	// non-empty line AND an error. Returning early on the error alone discards
	// an answer the operator actually gave: the prompt reads as unanswered, the
	// worktree stays held, and the command reports an abort over an explicit
	// confirmation. The read is therefore normalized FIRST and judged on the
	// same value the interpretation below uses: an errored read that carried
	// only whitespace holds no answer, so it stays unanswered instead of
	// selecting the default. A blank line with no error is the bare Enter the
	// [Y/n] hint documents, and still selects the default.
	line, err := promptReader().ReadString('\n')
	input := strings.TrimSpace(strings.ToLower(line))
	if err != nil && input == "" {
		return defaultYes, err
	}

	if input == "" {
		return defaultYes, nil
	}

	return input == "y" || input == "yes", nil
}
