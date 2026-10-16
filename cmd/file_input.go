package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/dynatrace-oss/dtctl/pkg/apply"
	"github.com/dynatrace-oss/dtctl/pkg/suggest"
)

// stdinSourceName is how input read from `-f -` is named wherever a command
// would otherwise show the file path: output labels and the apply hook's
// source-file argument.
const stdinSourceName = apply.StdinSourceFile

// readFileFlag reads the user-supplied input a file flag names: "-" is the
// process stdin, anything else a path through the vfs seam. Every command that
// reads a --file style flag goes through here, so `-f -` means stdin
// everywhere and a file literally named "-" is never read in its place
// (TestFileFlagsReadThroughReadFileFlag guards this).
//
// The returned error is the raw read error; callers keep their own wrapping.
func readFileFlag(ctx context.Context, flag, path string) ([]byte, error) {
	return readFileFlagFrom(ctx, flag, path, invocationStdin(ctx))
}

// sourceName is the name to show for the input a file flag names.
func sourceName(path string) string {
	if path == "-" {
		return stdinSourceName
	}
	return path
}

// readFileFlagFrom is readFileFlag with the stdin passed in, so the terminal
// case is testable without a pty -- the same shape resolveQueryInput uses.
//
// On a terminal, reading would block until Ctrl+D and then act on nothing,
// which reads as a hung CLI, so it fails fast instead. Empty piped input is
// rejected too: stdin can be read only once, so a second `-` in the same
// invocation, or a producer that wrote nothing, would otherwise surface as a
// confusing parse error far from the cause.
func readFileFlagFrom(ctx context.Context, flag, path string, stdin queryStdin) ([]byte, error) {
	if path != "-" {
		return vfsEnv(ctx).ReadFile(path)
	}
	if stdin.isTerminal {
		return nil, &suggest.FlagError{Flag: flag, Message: fmt.Sprintf(
			"--%s - reads from stdin, but stdin is a terminal -- nothing to read; pipe the input in (cat resource.yaml | dtctl ... --%s -) or pass a file path",
			flag, flag)}
	}
	data, err := io.ReadAll(stdin.r)
	if err != nil {
		return nil, fmt.Errorf("failed to read from stdin: %w", err)
	}
	if len(data) == 0 {
		return nil, errors.New("no input arrived on stdin -- the producing command wrote nothing, or stdin was already read by another flag")
	}
	return data, nil
}
