package reposcope

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The message of a refusal is printed, whatever check wrote it and whatever
// value that check formatted into it: no character of it acts on a terminal.
func TestInvalidError_ShowsItsWholeMessage(t *testing.T) {
	err := &InvalidError{Source: "a\x1b[2Jfile", Msg: "holds b\x1b[31m\a\rc"}
	require.Equal(t, "a�[2Jfile: holds b�[31m��c", err.Error())

	// A quoted value shows its escapes as text, and stays as written.
	err = invalid(`path "svc\x1b[31m" must not contain control or non-printing characters`)
	require.Equal(t, FileName+`: path "svc\x1b[31m" must not contain control or non-printing characters`, err.Error())
}
