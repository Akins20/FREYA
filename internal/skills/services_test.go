package skills

import (
	"context"
	"strings"
	"testing"

	"github.com/Akins20/FREYA/internal/routes"
)

// Learning where something lives is not the same as having used it.
//
// The first version stamped the success time at the moment of learning, so a
// brand-new route reported "last worked just now" having never been opened by
// anybody. That is the same unearned claim this codebase keeps finding: a fact
// about an event that did not happen, stated with the confidence of one that
// did. Until service_used says otherwise, the honest word is "learned".
func TestLearningARouteDoesNotClaimItHasWorked(t *testing.T) {
	r, _ := servicesOn(t)
	ctx := context.Background()

	out, err := r.Execute(ctx, "service_learn", map[string]any{
		"service": "email", "url": "https://mail.proton.me/u/0/inbox",
	})
	if err != nil {
		t.Fatalf("learn: %v", err)
	}
	if !strings.Contains(out, "Learned email") {
		t.Errorf("learning said %q", out)
	}

	out, err = r.Execute(ctx, "service_where", map[string]any{"service": "email"})
	if err != nil {
		t.Fatalf("where: %v", err)
	}
	if strings.Contains(out, "worked") {
		t.Errorf("a route nobody has used claims to have worked:\n%s", out)
	}
	if !strings.Contains(out, "learned") {
		t.Errorf("the age does not say it was merely learned:\n%s", out)
	}
	// And it must not claim anybody opened it, which the first version did on
	// the strength of the host matching a list.
	if strings.Contains(out, "confirmed by opening") {
		t.Errorf("recording an address claimed somebody opened it:\n%s", out)
	}

	// Once it has actually been used, it may say so.
	if _, err := r.Execute(ctx, "service_used", map[string]any{
		"service": "email", "worked": true,
	}); err != nil {
		t.Fatalf("used: %v", err)
	}
	out, err = r.Execute(ctx, "service_where", map[string]any{"service": "email"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "worked") {
		t.Errorf("a route that was used does not say so:\n%s", out)
	}
}

// A service she has not learned is a refusal that says how to learn it, rather
// than a guess at an address.
//
// Guessing here is the expensive failure: a wrong inbox address does not error,
// it loads something, and "your mail is empty" is the answer that comes back.
func TestAnUnknownServiceRefusesAndSaysWhatToDo(t *testing.T) {
	r, _ := servicesOn(t)
	_, err := r.Execute(context.Background(), "service_where", map[string]any{
		"service": "calendar",
	})
	if err == nil {
		t.Fatal("an unlearned service produced an answer")
	}
	if !strings.Contains(err.Error(), "service_find") {
		t.Errorf("the refusal does not say how to learn it: %v", err)
	}
}

// Every answer carries the instruction to check where it landed, because a
// remembered address that has rotted looks like an empty inbox and not an error.
func TestAnAddressComesWithTheInstructionToCheckIt(t *testing.T) {
	r, _ := servicesOn(t)
	ctx := context.Background()
	if _, err := r.Execute(ctx, "service_learn", map[string]any{
		"service": "email", "url": "https://mail.example.com/inbox",
	}); err != nil {
		t.Fatal(err)
	}
	out, err := r.Execute(ctx, "service_where", map[string]any{"service": "email"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"mail.example.com", "auth", "Check the page"} {
		if !strings.Contains(out, want) {
			t.Errorf("the answer is missing %q:\n%s", want, out)
		}
	}
}

func servicesOn(t *testing.T) (*Registry, *routes.Store) {
	t.Helper()
	store, err := routes.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := New()
	// nil guard and tabs: these tests are about the memory, and service_open is
	// only registered when there is a browser to open things in.
	RegisterServices(r, nil, nil, store)
	return r, store
}
