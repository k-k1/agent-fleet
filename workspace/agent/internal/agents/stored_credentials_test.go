package agents

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
)

// fakeLogin is a CLI login a test edits: present with a binding, and a logout that may fail.
// unknownAfter makes every read after the logout fail (a status probe that errs).
type fakeLogin struct {
	present      bool
	unknownAfter bool
	binding      string
	routes       []string
	logoutOK     bool
	removes      int
	during       func() // runs inside the logout, while the login is held
}

func (f *fakeLogin) removal() LoginRemoval {
	return LoginRemoval{
		Login: "test-login",
		Current: func(context.Context) ([]string, string, LoginState) {
			switch {
			case f.unknownAfter && f.removes > 0:
				return nil, "", LoginUnknown
			case !f.present:
				return nil, "", LoginAbsent
			}
			return f.routes, f.binding, LoginPresent
		},
		Remove: func(context.Context) error {
			f.removes++
			if f.during != nil {
				f.during()
			}
			if !f.logoutOK {
				return errors.New("logout: exit status 1")
			}
			f.present = false
			return nil
		},
	}
}

func always([]string, string) bool { return true }

// A login is removed only while it is still the confirmed one and forbidden, and only a removal
// that leaves it absent counts: a logout that fails, or exits cleanly and leaves the login, is a
// failure, never a removal.
func TestRemoveLogin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	credbind.ResetForTest()
	ctx := context.Background()
	mk := func() *fakeLogin {
		return &fakeLogin{present: true, binding: "b1", routes: []string{"r"}, logoutOK: true}
	}

	f := mk()
	f.binding = "b2" // replaced since it was confirmed
	if err := RemoveLogin(ctx, f.removal(), "b1", always); !errors.Is(err, ErrCredentialChanged) || f.removes != 0 {
		t.Errorf("replaced: %v, removes %d", err, f.removes)
	}
	f = mk()
	if err := RemoveLogin(ctx, f.removal(), "b1", func([]string, string) bool { return false }); !errors.Is(err, ErrCredentialChanged) || f.removes != 0 {
		t.Errorf("no longer forbidden: %v, removes %d", err, f.removes)
	}
	f = mk()
	f.present = false
	if err := RemoveLogin(ctx, f.removal(), "b1", always); !errors.Is(err, ErrCredentialChanged) || f.removes != 0 {
		t.Errorf("gone: %v", err)
	}

	// A sign-in in flight (its change begun, not finished) is never raced.
	f = mk()
	op, err := credbind.Begin("test-login")
	if err != nil {
		t.Fatal(err)
	}
	if err := RemoveLogin(ctx, f.removal(), "b1", always); !errors.Is(err, ErrCredentialChanged) || f.removes != 0 {
		t.Errorf("with a sign-in in flight: %v, removes %d", err, f.removes)
	}
	op.Finish()

	// The failing-logout control: the CLI errs and the login stays.
	f = mk()
	f.logoutOK = false
	if err := RemoveLogin(ctx, f.removal(), "b1", always); !errors.Is(err, ErrStillPresent) {
		t.Errorf("failed logout: %v, want ErrStillPresent", err)
	}
	// A logout that exits cleanly and leaves the login is a failure too.
	f = mk()
	r := f.removal()
	r.Remove = func(context.Context) error { return nil }
	if err := RemoveLogin(ctx, r, "b1", always); !errors.Is(err, ErrStillPresent) {
		t.Errorf("logout that did nothing: %v", err)
	}

	// A logout whose outcome cannot be read (the probe fails afterwards) is a failure, never a
	// removal, whether the CLI erred or not: the login may well still be there.
	f = mk()
	f.logoutOK, f.unknownAfter = false, true
	if err := RemoveLogin(ctx, f.removal(), "b1", always); !errors.Is(err, ErrAbsenceUnknown) {
		t.Errorf("failed logout, failed probe: %v, want ErrAbsenceUnknown", err)
	}
	f = mk()
	f.unknownAfter = true
	if err := RemoveLogin(ctx, f.removal(), "b1", always); !errors.Is(err, ErrAbsenceUnknown) {
		t.Errorf("clean logout, failed probe: %v, want ErrAbsenceUnknown", err)
	}
	// A login whose state cannot be read before the removal is not touched.
	r = f.removal()
	r.Current = func(context.Context) ([]string, string, LoginState) { return nil, "", LoginUnknown }
	f.removes = 0
	if err := RemoveLogin(ctx, r, "b1", always); !errors.Is(err, ErrCredentialChanged) || f.removes != 0 {
		t.Errorf("unknown before: %v, removes %d", err, f.removes)
	}

	// A sign-in that starts while the removal runs waits until it is over.
	f = mk()
	began := make(chan struct{})
	f.during = func() {
		go func() {
			o, err := credbind.Begin("test-login")
			if err == nil {
				o.Finish()
			}
			close(began)
		}()
		select {
		case <-began:
			t.Error("a sign-in began while the login was being removed")
		case <-time.After(100 * time.Millisecond):
		}
	}
	if err := RemoveLogin(ctx, f.removal(), "b1", always); err != nil || f.present {
		t.Errorf("control: %v, present %v", err, f.present)
	}
	<-began
}

func TestJSONLoginState(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/auth.json"
	read := func(b []byte) routes.Launch {
		if string(b) == "{}" {
			return routes.Launch{}
		}
		return routes.Launch{Route: "r", Binding: "b"}
	}
	if _, _, st := JSONLoginState(p, read); st != LoginAbsent {
		t.Errorf("no file: %v", st)
	}
	for body, want := range map[string]LoginState{"{}": LoginAbsent, `{"k":1}`: LoginPresent, `{"k":`: LoginUnknown} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, st := JSONLoginState(p, read); st != want {
			t.Errorf("%q: %v, want %v", body, st, want)
		}
	}
}
