package gateway

import (
	"net/http"
	"testing"
	"time"

	"github.com/barockok/proem/internal/client"
)

func TestRefreshWithoutRotation(t *testing.T) {
	h := newHarness(t)
	tr := h.login()
	code, out := h.refreshWith(tr.RefreshToken)
	if code != http.StatusOK || out.AccessToken == "" || out.RefreshToken == "" {
		t.Fatalf("refresh: %d %+v", code, out)
	}
	if _, err := h.gw.Sessions().Verify(out.AccessToken); err != nil {
		t.Fatal(err)
	}
	// The old blob still works: the IdP did not rotate.
	if code, _ := h.refreshWith(tr.RefreshToken); code != http.StatusOK {
		t.Fatalf("old blob after non-rotating refresh: %d", code)
	}
}

func TestRefreshWithRotation(t *testing.T) {
	h := newHarness(t)
	h.idp.set(func(f *fakeIdP) { f.rotate = true })
	tr := h.login()
	code, out := h.refreshWith(tr.RefreshToken)
	if code != http.StatusOK {
		t.Fatalf("refresh: %d %+v", code, out)
	}
	blob, err := h.gw.refresh.open(out.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	old, _ := h.gw.refresh.open(tr.RefreshToken)
	if blob.IDPRefreshToken == old.IDPRefreshToken {
		t.Fatal("rotated IdP refresh token must be stored in the new blob")
	}
	if blob.AuthTime != old.AuthTime {
		t.Fatal("auth_time must carry over unchanged")
	}
	if code, _ := h.refreshWith(out.RefreshToken); code != http.StatusOK {
		t.Fatalf("refresh with rotated blob: %d", code)
	}
}

func TestRefreshUsesUserinfoWithoutIDToken(t *testing.T) {
	h := newHarness(t)
	h.idp.set(func(f *fakeIdP) { f.noIDOnRefresh = true })
	tr := h.login()
	if code, out := h.refreshWith(tr.RefreshToken); code != http.StatusOK {
		t.Fatalf("refresh via userinfo: %d %+v", code, out)
	}
	h.idp.set(func(f *fakeIdP) { f.userinfoDown = true })
	if code, out := h.refreshWith(tr.RefreshToken); code != http.StatusServiceUnavailable || out.Error != errTemporarilyUnavailable {
		t.Fatalf("userinfo down: %d %+v", code, out)
	}
}

func TestRefreshRejections(t *testing.T) {
	h := newHarness(t)
	tr := h.login()

	if code, out := h.refreshWith("garbage"); code != http.StatusBadRequest || out.Error != errInvalidGrant {
		t.Fatalf("garbage: %d %+v", code, out)
	}

	// IdP down -> 503, the client keeps its login.
	h.idp.set(func(f *fakeIdP) { f.down = true })
	if code, out := h.refreshWith(tr.RefreshToken); code != http.StatusServiceUnavailable || out.Error != errTemporarilyUnavailable {
		t.Fatalf("idp down: %d %+v", code, out)
	}
	h.idp.set(func(f *fakeIdP) { f.down = false })

	// Subject changed at the IdP.
	h.idp.set(func(f *fakeIdP) { f.refreshSub = "someone-else" })
	if _, out := h.refreshWith(tr.RefreshToken); out.Error != errInvalidGrant {
		t.Fatalf("sub mismatch: %+v", out)
	}
	h.idp.set(func(f *fakeIdP) { f.refreshSub = "" })

	// Client disabled in clients.yaml.
	h.reg.set(t, client.Client{Name: "alice", Email: "alice@example.com", Enabled: disabled()})
	if _, out := h.refreshWith(tr.RefreshToken); out.Error != errInvalidGrant {
		t.Fatalf("disabled client: %+v", out)
	}
	h.reg.set(t, defaultClients()...)
	if code, _ := h.refreshWith(tr.RefreshToken); code != http.StatusOK {
		t.Fatalf("restored client: %d", code)
	}

	// User disabled at the IdP: its refresh tokens are revoked.
	h.idp.revoke()
	if _, out := h.refreshWith(tr.RefreshToken); out.Error != errInvalidGrant {
		t.Fatalf("revoked at IdP: %+v", out)
	}
}

func TestRefreshRechecksEmailVerified(t *testing.T) {
	h := newHarness(t)
	tr := h.login()
	// The IdP now reports the email as unverified on refresh.
	h.idp.set(func(f *fakeIdP) {
		for k, u := range f.refreshes {
			u.EmailVerified = false
			f.refreshes[k] = u
		}
	})
	if _, out := h.refreshWith(tr.RefreshToken); out.Error != errInvalidGrant {
		t.Fatalf("unverified on refresh: %+v", out)
	}
}

func TestRefreshKeepsLoginEmailWhenIdPOmitsIt(t *testing.T) {
	h := newHarness(t)
	tr := h.login()
	h.idp.set(func(f *fakeIdP) {
		for k, u := range f.refreshes {
			u.Email, u.EmailVerified = "", nil
			f.refreshes[k] = u
		}
	})
	code, out := h.refreshWith(tr.RefreshToken)
	if code != http.StatusOK {
		t.Fatalf("refresh: %d %+v", code, out)
	}
	c, _ := h.gw.Sessions().Verify(out.AccessToken)
	if c.Email != "alice@example.com" {
		t.Fatalf("email %q", c.Email)
	}
}

func TestSessionMax(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.SessionMax = 2 * time.Hour })
	tr := h.login()
	h.advance(90 * time.Minute)
	code, out := h.refreshWith(tr.RefreshToken)
	if code != http.StatusOK {
		t.Fatalf("refresh inside session-max: %d %+v", code, out)
	}
	h.advance(31 * time.Minute)
	if code, out := h.refreshWith(out.RefreshToken); code != http.StatusBadRequest || out.Error != errInvalidGrant {
		t.Fatalf("refresh after session-max: %d %+v", code, out)
	}
}
