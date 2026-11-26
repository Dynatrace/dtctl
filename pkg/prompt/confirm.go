package prompt

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// Confirm prompts the user for yes/no confirmation
// Returns true if user confirms, false otherwise
func Confirm(message string) bool {
	return ConfirmWith(os.Stdin, os.Stdout, message)
}

// ConfirmWith is Confirm on the given streams, for a caller that owns the
// invocation's input and output instead of the process's. It buffers in
// per call, so pass a *bufio.Reader when confirming more than once on one in.
func ConfirmWith(in io.Reader, out io.Writer, message string) bool {
	fmt.Fprintf(out, "%s [y/N]: ", message)

	reader := bufio.NewReader(in)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}

	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes"
}

// ConfirmDeletion prompts for confirmation of a destructive operation
// Shows resource details and requires explicit confirmation
func ConfirmDeletion(resourceType, name, id string) bool {
	return ConfirmDeletionWith(os.Stdin, os.Stdout, resourceType, name, id)
}

// ConfirmDeletionWith is ConfirmDeletion on the given streams.
func ConfirmDeletionWith(in io.Reader, out io.Writer, resourceType, name, id string) bool {
	fmt.Fprintf(out, "\nYou are about to delete the following %s:\n", resourceType)
	fmt.Fprintf(out, "  Name: %s\n", name)
	fmt.Fprintf(out, "  ID:   %s\n", id)
	fmt.Fprintln(out)

	return ConfirmWith(in, out, "Are you sure you want to delete this resource?")
}

// ConfirmDataDeletion prompts for confirmation of an irreversible data operation
// Requires the user to type the resource name exactly to confirm
// Returns true if confirmed, false otherwise
func ConfirmDataDeletion(resourceType, name string) bool {
	return ConfirmDataDeletionWith(os.Stdin, os.Stdout, resourceType, name)
}

// ConfirmDataDeletionWith is ConfirmDataDeletion on the given streams.
func ConfirmDataDeletionWith(in io.Reader, out io.Writer, resourceType, name string) bool {
	fmt.Fprintf(out, "\n⚠️  WARNING: This operation is IRREVERSIBLE and will delete all data\n")
	fmt.Fprintf(out, "  Resource Type: %s\n", resourceType)
	fmt.Fprintf(out, "  Name:          %s\n", name)
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Type the %s name '%s' to confirm: ", resourceType, name)

	reader := bufio.NewReader(in)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}

	response = strings.TrimSpace(response)
	return response == name
}

// ValidateConfirmFlag checks if the --confirm flag value matches the resource name
// Used for non-interactive confirmation of data deletion
func ValidateConfirmFlag(confirmValue, resourceName string) bool {
	return confirmValue == resourceName
}
