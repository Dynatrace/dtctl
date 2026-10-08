// Package reposcope links a git repository to the Dynatrace entities that run
// its code. The link is a data-only file at the repository root,
// .dtctl-repo-scope.yaml: structured bindings per environment host, never
// free-form DQL. dtctl renders the DQL filter from the bindings itself, with
// every literal validated and quoted, so a cloned repository can narrow a
// query but never widen it, redirect it, or inject text into it.
//
// The package has no view of the invocation: it reads the file it is pointed
// at, resolves an entry for a directory, reads names from an fs.FS and runs
// discovery through an inventory.Runner. Where the working directory comes
// from, whether the host grants access to it, and how results are printed
// are the caller's.
package reposcope
