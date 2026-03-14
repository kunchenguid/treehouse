package ui

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func Confirm(message string, defaultYes bool) (bool, error) {
	hint := "Y/n"
	if !defaultYes {
		hint = "y/N"
	}

	fmt.Fprintf(os.Stderr, "%s [%s] ", message, hint)

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return defaultYes, err
	}

	input = strings.TrimSpace(strings.ToLower(input))
	if input == "" {
		return defaultYes, nil
	}

	return input == "y" || input == "yes", nil
}
