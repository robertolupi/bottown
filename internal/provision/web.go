package provision

import (
	"context"
	"errors"
	"net/http"

	"github.com/robertolupi/bottown/internal/forgejo"
	"github.com/robertolupi/bottown/internal/secrets"
)

// ErrOwnerPassword means bottown cannot sign the owner in to the web UI
// because the password it holds is missing or was changed in Forgejo.
var ErrOwnerPassword = errors.New("bottown does not hold the owner's current Forgejo password; retry with --reset-password")

// OwnerWebSession signs the owner in to Forgejo's web UI with the password
// bottown keeps, and returns the session cookie. With reset, it first sets a
// new random password through the admin CLI and returns it too, so the host
// can update its copy.
func (r *Reconciler) OwnerWebSession(ctx context.Context, cookieName string, reset bool) (*http.Cookie, string, error) {
	var newPassword string
	if reset {
		newPassword = secrets.RandomString(18)
		if err := r.Admin.ChangePassword(ctx, r.Owner.Login, newPassword); err != nil {
			return nil, "", err
		}
		if err := r.Store.Update(func(s *secrets.State) error { s.OwnerPassword = newPassword; return nil }); err != nil {
			return nil, "", err
		}
	}
	st, err := r.Store.Read()
	if err != nil {
		return nil, "", err
	}
	if st.OwnerPassword == "" {
		return nil, "", ErrOwnerPassword
	}
	c, err := forgejo.WebSession(ctx, r.BaseURL, cookieName, r.Owner.Login, st.OwnerPassword)
	if errors.Is(err, forgejo.ErrBadCredentials) {
		return nil, "", ErrOwnerPassword
	}
	return c, newPassword, err
}
