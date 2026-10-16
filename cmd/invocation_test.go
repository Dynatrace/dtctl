package cmd

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// unrelatedKey is a context key that has nothing to do with the invocation.
type unrelatedKey struct{}

// TestInvocationTravelsOnTheContext pins how an invocation is found: through the
// context it was attached to, from every context derived from that one and from
// every goroutine handed such a context, and from nothing else. A context that
// carries no invocation — the plain CLI, a test — has none, and a serialized
// invocation is never reported as concurrent.
func TestInvocationTravelsOnTheContext(t *testing.T) {
	if got := concurrentActive.Load(); got != 0 {
		t.Fatalf("concurrentActive = %d before the test, want 0", got)
	}
	if current(nil) != nil { //nolint:staticcheck // the accessors must tolerate a nil context
		t.Fatal("found an invocation in a nil context")
	}
	if current(context.Background()) != nil || concurrentInvocation(context.Background()) != nil {
		t.Fatal("found an invocation in a context that carries none")
	}

	inv := &invocation{concurrent: true}
	ctx := withInvocation(context.Background(), inv)
	if got := current(ctx); got != inv {
		t.Errorf("current(ctx) = %p, want the attached %p", got, inv)
	}
	if got := concurrentInvocation(ctx); got != inv {
		t.Errorf("concurrentInvocation(ctx) = %p, want %p", got, inv)
	}

	derived, cancel := context.WithCancel(context.WithValue(ctx, unrelatedKey{}, "unrelated"))
	defer cancel()
	if current(derived) != inv {
		t.Error("a context derived from the invocation's lost it")
	}

	var wg sync.WaitGroup
	found := make(chan *invocation, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		found <- current(derived)
	}()
	wg.Wait()
	if got := <-found; got != inv {
		t.Error("a goroutine given the invocation's context did not find it")
	}

	other := &invocation{concurrent: true}
	if current(withInvocation(context.Background(), other)) == inv {
		t.Error("an unrelated context found another invocation")
	}

	serialized := withInvocation(context.Background(), &invocation{})
	if concurrentInvocation(serialized) != nil {
		t.Error("a serialized invocation was reported as concurrent")
	}
}

// TestEndedInvocationIsInvisible holds the other half of an invocation's
// lifetime. cobra keeps the context a serialized Run bound the singleton tree
// to, so a context can outlive its invocation; a command called directly after
// Run returns must not find that invocation's session, streams or environment.
func TestEndedInvocationIsInvisible(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"version"}, RunOptions{
		Session: &Session{EnvironmentURL: "https://env.example.test", Token: "dt0c01.STALE.TOKEN"},
		Stdout:  &stdout,
		Stderr:  &stderr,
	})
	require.Equal(t, 0, code, "stderr: %s", stderr.String())

	ctx := cmdContext(rootCmd)
	require.Nil(t, current(ctx), "the finished invocation is still reachable through the singleton tree's context")
	require.Nil(t, currentSession(ctx), "a later caller would resolve the finished invocation's session")
}
