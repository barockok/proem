package gateway

import (
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
)

// handleCallback completes the IdP leg: it checks state against the grant
// bound to this browser by the gw_dev cookie, exchanges the code with the
// PKCE verifier, verifies the ID token and nonce, resolves the client by
// email and marks the grant approved or denied.
func (g *Gateway) handleCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cookie, err := r.Cookie(deviceCookie)
	if err != nil || cookie.Value == "" {
		g.fail(r, "callback_no_session")
		messagePage(w, http.StatusBadRequest, "Sign-in not started here",
			"Open the link from Claude Code in this browser and try again.")
		return
	}
	dcHash := cookie.Value
	gr, err := g.grants.byHash(ctx, dcHash)
	if err != nil {
		g.deviceLookupFailed(w, r, err)
		return
	}
	q := r.URL.Query()
	state := q.Get("state")
	if gr.Status != statusPending || gr.State == "" || state == "" ||
		subtle.ConstantTimeCompare([]byte(state), []byte(gr.State)) != 1 {
		g.fail(r, "callback_state_mismatch")
		messagePage(w, http.StatusBadRequest, "Sign-in not valid",
			"This sign-in response does not match your request. Run /login in Claude Code again.")
		return
	}
	g.clearDeviceCookie(w)

	if idpErr := q.Get("error"); idpErr != "" {
		g.fail(r, "sso_idp_error", slog.String("idp_error", idpErr))
		g.deny(w, r, dcHash, gr, "Sign-in cancelled", "The identity provider did not complete the sign-in.")
		return
	}

	id, idpRefresh, err := g.provider.Exchange(ctx, q.Get("code"), gr.PKCEVerifier, gr.Nonce)
	if errors.Is(err, errIdPUnavailable) {
		g.logger.LogAttrs(ctx, slog.LevelError, "identity provider unavailable", slog.String("error", err.Error()))
		messagePage(w, http.StatusServiceUnavailable, "Sign-in unavailable",
			"The identity provider cannot be reached. Run /login in Claude Code again in a few minutes.")
		return
	}
	if err != nil {
		g.fail(r, "sso_exchange_rejected", slog.String("error", err.Error()))
		g.deny(w, r, dcHash, gr, "Sign-in failed", "The identity provider sign-in could not be verified.")
		return
	}
	if reason, msg := checkIdentity(id, g.cfg.AllowedDomain); reason != "" {
		g.fail(r, reason, slog.String("email", id.Email))
		g.deny(w, r, dcHash, gr, "Access denied", msg)
		return
	}
	c, ok := g.clients.Active().LookupEmail(id.Email)
	if !ok || !c.IsEnabled() {
		reason := "unknown_email"
		if ok {
			reason = "client_disabled"
		}
		g.fail(r, reason, slog.String("email", id.Email))
		g.deny(w, r, dcHash, gr, "Access denied", "This account is not registered with proem. Ask an admin to add it.")
		return
	}

	next := gr
	next.Email, next.Sub = id.Email, id.Subject
	if idpRefresh != "" {
		next.EncRefresh = g.refresh.seal(refreshPayload{
			IDPRefreshToken: idpRefresh, Sub: id.Subject, Email: id.Email, AuthTime: g.now().Unix(),
		})
	}
	if err := g.grants.finish(ctx, dcHash, next, statusApproved); err != nil {
		g.deviceLookupFailed(w, r, err)
		return
	}
	g.logger.LogAttrs(ctx, slog.LevelInfo, "sso login approved",
		slog.String("client", c.Name), slog.String("ip", gr.RequesterIP))
	messagePage(w, http.StatusOK, "Signed in", "Signed in as "+id.Email+". Return to Claude Code.")
}

// deny marks the grant denied, so the polling client gets access_denied, and
// renders the reason.
func (g *Gateway) deny(w http.ResponseWriter, r *http.Request, dcHash string, gr grant, title, message string) {
	if err := g.grants.finish(r.Context(), dcHash, gr, statusDenied); err != nil {
		g.deviceLookupFailed(w, r, err)
		return
	}
	messagePage(w, http.StatusForbidden, title, message)
}

func (g *Gateway) clearDeviceCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: deviceCookie, Value: "", Path: callbackPath, MaxAge: -1,
		HttpOnly: true, Secure: g.secureCookies(), SameSite: http.SameSiteLaxMode,
	})
}
