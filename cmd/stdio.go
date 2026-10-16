package cmd

import (
	"context"
	"io"
	"os"
	"sync"
)

// redirectStdio routes the process standard streams to the invocation's
// writers/reader for the duration of a Run. Swapping os.Stdout/os.Stderr/
// os.Stdin (rather than threading writers through every command) catches
// every output path at once — fmt.Print*, the output package, cobra help,
// error envelopes — which is exactly the CLI-identical byte stream the
// service contract wants (docs/dev/SERVICE_ENGINE_DESIGN.md). Safe for a
// serialized invocation because it holds runMu exclusively and the
// pristine-tree restore clears any cobra-bound writers; a concurrent
// invocation never swaps the process streams (below).
//
// The returned cleanup restores the process streams and blocks until all
// piped output has been drained into the destination writers.
func redirectStdio(ctx context.Context, stdout, stderr io.Writer, stdin io.Reader) (cleanup func(), err error) {
	if stdout == nil && stderr == nil && stdin == nil {
		return func() {}, nil
	}

	// A concurrent invocation must not
	// touch the process streams — there is one os.Stdout and several
	// invocations, so the last one to redirect would collect everybody's
	// output. Record the writers on the invocation instead and let
	// currentStdout/currentStderr/currentStdin route each write to the
	// invocation its context carries.
	//
	// Output that does not go through those accessors lands on the real
	// process stdout instead: lost from the response rather than delivered
	// into another tenant's, but written to the host's log, so it is not a
	// harmless failure mode. TestNoProcessStreamWritesOnRequestPaths keeps
	// every write in cmd/ and pkg/ on the accessors.
	if inv := current(ctx); inv != nil && inv.concurrent {
		inv.stdout, inv.stderr, inv.stdin = stdout, stderr, stdin
		return func() {}, nil
	}

	var (
		restores []func()
		drain    sync.WaitGroup
	)
	fail := func(err error) (func(), error) {
		for i := len(restores) - 1; i >= 0; i-- {
			restores[i]()
		}
		return nil, err
	}

	pipeOut := func(target **os.File, dest io.Writer) error {
		r, w, err := os.Pipe()
		if err != nil {
			return err
		}
		orig := *target
		*target = w
		drain.Add(1)
		go func() {
			defer drain.Done()
			_, _ = io.Copy(dest, r)
			_ = r.Close()
		}()
		restores = append(restores, func() {
			*target = orig
			// Closing the write end delivers EOF to the drain goroutine.
			_ = w.Close()
		})
		return nil
	}

	if stdout != nil {
		if err := pipeOut(&os.Stdout, stdout); err != nil {
			return fail(err)
		}
	}
	if stderr != nil {
		if err := pipeOut(&os.Stderr, stderr); err != nil {
			return fail(err)
		}
	}
	if stdin != nil {
		r, w, err := os.Pipe()
		if err != nil {
			return fail(err)
		}
		orig := os.Stdin
		os.Stdin = r
		go func() {
			// Feed the request stdin; close on EOF so "-" readers terminate.
			// If the command never reads, cleanup's r.Close() unblocks the
			// copy with a write-on-closed-pipe error and the goroutine exits.
			_, _ = io.Copy(w, stdin)
			_ = w.Close()
		}()
		restores = append(restores, func() {
			os.Stdin = orig
			_ = r.Close()
		})
	}

	return func() {
		for i := len(restores) - 1; i >= 0; i-- {
			restores[i]()
		}
		drain.Wait()
	}, nil
}
