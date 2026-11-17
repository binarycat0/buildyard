package httpapi

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"strconv"
)

// CookieName holds the API token in the browser.
const CookieName = "buildyard_token"

// HeaderName carries the API token for non-browser clients (CLI, scripts).
const HeaderName = "X-Buildyard-Token"

func itoa(n int) string { return strconv.Itoa(n) }

// secure applies, in order:
//  1. Host check: only 127.0.0.1:<port> or localhost:<port> (DNS rebinding) → 403.
//  2. Token login: GET ?token=<valid> sets an HttpOnly SameSite=Strict cookie and redirects without it.
//  3. Token check: cookie or X-Buildyard-Token header → otherwise 401 (API) or a short help page (UI).
//  4. Origin check on state-changing requests (CSRF) → 403. A request authenticated by the
//     header may omit Origin (CLI); a cookie-authenticated one must send a matching Origin.
func (s *Server) secure(next http.Handler) http.Handler {
	hosts := map[string]bool{
		"127.0.0.1:" + itoa(s.Port): true,
		"localhost:" + itoa(s.Port): true,
	}
	origins := map[string]bool{
		"http://127.0.0.1:" + itoa(s.Port): true,
		"http://localhost:" + itoa(s.Port): true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hosts[r.Host] {
			writeError(w, http.StatusForbidden, "bad_host", "requests must use 127.0.0.1 or localhost")
			return
		}

		if q := r.URL.Query(); r.Method == http.MethodGet && q.Has("token") {
			if !s.tokenOK(q.Get("token")) {
				writeError(w, http.StatusUnauthorized, "bad_token", "invalid token")
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name: CookieName, Value: s.Token, Path: "/",
				HttpOnly: true, SameSite: http.SameSiteStrictMode,
			})
			q.Del("token")
			u := url.URL{Path: r.URL.Path, RawQuery: q.Encode()}
			http.Redirect(w, r, u.String(), http.StatusSeeOther)
			return
		}

		viaHeader := s.tokenOK(r.Header.Get(HeaderName))
		viaCookie := false
		if c, err := r.Cookie(CookieName); err == nil {
			viaCookie = s.tokenOK(c.Value)
		}
		if !viaHeader && !viaCookie {
			if isAPI(r) {
				writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid token")
			} else {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte("Buildyard: open the link printed by `buildyard serve` or `buildyard open`.\n"))
			}
			return
		}

		if !safeMethod(r.Method) {
			origin := r.Header.Get("Origin")
			switch {
			case origin != "" && !origins[origin]:
				writeError(w, http.StatusForbidden, "bad_origin", "cross-origin request refused")
				return
			case origin == "" && !viaHeader:
				writeError(w, http.StatusForbidden, "bad_origin", "Origin header required")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) tokenOK(got string) bool {
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(s.Token)) == 1
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func isAPI(r *http.Request) bool {
	return len(r.URL.Path) >= 5 && r.URL.Path[:5] == "/api/"
}
