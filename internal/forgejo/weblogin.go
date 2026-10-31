package forgejo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ErrBadCredentials means Forgejo rejected the login form.
var ErrBadCredentials = errors.New("forgejo rejected the username or password")

var csrfRE = regexp.MustCompile(`name="_csrf"\s+value="([^"]+)"`)

// WebSession signs username in through Forgejo's login form, as a browser
// would, and returns the session cookie (named cookieName) that Forgejo set.
// Forgejo has no API to mint a web session, so this is the only way to hand
// one to the owner's browser without them typing the password.
func WebSession(ctx context.Context, baseURL, cookieName, username, password string) (*http.Cookie, error) {
	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return nil, err
	}
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{
		Jar:     jar,
		Timeout: 15 * time.Second,
		// Success is a redirect away from the form; don't follow it.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	loginURL := base.String() + "/user/login"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, loginURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	page, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	form := url.Values{"user_name": {username}, "password": {password}}
	// Older Forgejo/Gitea embed a _csrf token in the form; Forgejo 15 has none
	// favour of Origin/Sec-Fetch-Site checks, which a server-side request
	// without those headers passes.
	if m := csrfRE.FindSubmatch(page); m != nil {
		form.Set("_csrf", string(m[1]))
	} else if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("login page %s: %s", loginURL, resp.Status)
	}

	req, err = http.NewRequestWithContext(ctx, http.MethodPost, loginURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err = hc.Do(req)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil, ErrBadCredentials // the form was re-rendered with an error
	case resp.StatusCode/100 != 3:
		return nil, fmt.Errorf("login: unexpected %s", resp.Status)
	}
	for _, c := range jar.Cookies(base) {
		if c.Name == cookieName {
			return &http.Cookie{Name: c.Name, Value: c.Value}, nil
		}
	}
	return nil, fmt.Errorf("login succeeded but no %s cookie was set", cookieName)
}
