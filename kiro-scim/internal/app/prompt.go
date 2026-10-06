package app

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// promptYesNo asks the operator a yes/no question on stdin and returns true
// only for an explicit affirmative ("y" or "yes", case-insensitive). Anything
// else, including EOF, is treated as "no".
func promptYesNo(prompt string) (bool, error) {
	fmt.Printf("%s [y/N]: ", prompt)
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return false, err
		}
		return false, nil // EOF -> no
	}
	answer := strings.ToLower(strings.TrimSpace(sc.Text()))
	return answer == "y" || answer == "yes", nil
}
