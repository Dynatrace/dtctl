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
//
// A program other than dtctl that wants the same scope reads it through this
// package rather than parsing the file: Locate, Load, Resolve and Filters,
// then dql.InsertFilter (see the package example). Those calls only read.
// Save is the one function that writes, and Discover the one that queries an
// environment.
//
// Load returns an empty File for a repository that has none, so that a
// caller which saves can start from it. A caller that reads checks
// Location.Exists first: resolving the empty File answers "no entry for
// this environment", which is true and is not what a repository without a
// file should be told. Status.Code says which answer a Status is.
//
// The Go API is experimental, like the commands built on it. It may change
// in any release, and the package may be split: reading a scope and
// discovering one live here together for the first release only. The file
// format is the contract.
package reposcope
