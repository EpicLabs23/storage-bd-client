package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// Secrets never come from command-line arguments: argv is visible to
// every local user via `ps` and /proc (§19). They're read from a file
// (--x-file PATH, "-" for stdin) or an interactive no-echo prompt.

// readSecret resolves one secret from filePath, else prompts on the TTY.
func readSecret(filePath, prompt string) (string, error) {
	if filePath != "" {
		return readSecretFile(filePath)
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("%s: stdin is not a terminal; pass it with a --*-file flag", strings.TrimSuffix(prompt, ": "))
	}
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// readSecretFile reads the first line of path ("-" = stdin).
func readSecretFile(path string) (string, error) {
	var r io.Reader
	if path == "-" {
		r = os.Stdin
	} else {
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer f.Close()
		r = f
	}
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", fmt.Errorf("no secret found in %s", path)
	}
	return line, nil
}

// confirm asks a yes/no question on the TTY. Non-interactive callers must
// pass --yes instead; refusing is safer than assuming.
func confirm(question string) (bool, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false, errors.New("confirmation required; re-run with --yes")
	}
	fmt.Fprintf(os.Stderr, "%s [y/N]: ", question)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

// promptLine reads one visible line from the TTY (non-secret input).
func promptLine(prompt string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("%s: stdin is not a terminal; pass it as a flag", strings.TrimSuffix(prompt, ": "))
	}
	fmt.Fprint(os.Stderr, prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
