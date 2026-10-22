package web

import (
	"fmt"
	"net/http"
	"strings"

	"photothing/internal/store"
)

// Supported interface languages. English texts are the keys; "nl" maps them to Dutch.
// A text without a translation is shown in English (a test checks that none are missing).
var langs = []string{"en", "nl"}

func validLang(l string) bool {
	for _, x := range langs {
		if x == l {
			return true
		}
	}
	return false
}

func tr(lang, key string, args ...any) string {
	text := key
	if lang == "nl" {
		if v, ok := nl[key]; ok {
			text = v
		}
	}
	if len(args) > 0 {
		return fmt.Sprintf(text, args...)
	}
	return text
}

// T translates key (with fmt arguments) into the language of the request.
func (s *Server) T(r *http.Request, key string, args ...any) string {
	return tr(s.state(r).Lang, key, args...)
}

// pickLang: the user's saved choice, else the language cookie, else the browser's Accept-Language, else English.
func (s *Server) pickLang(r *http.Request, u *store.User) string {
	if u != nil && validLang(u.Language) {
		return u.Language
	}
	if c, err := r.Cookie(langCookie); err == nil && validLang(c.Value) {
		return c.Value
	}
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag, _, _ := strings.Cut(strings.TrimSpace(part), ";")
		primary, _, _ := strings.Cut(strings.ToLower(tag), "-")
		if validLang(primary) {
			return primary
		}
	}
	return "en"
}

func (s *Server) setLangCookie(w http.ResponseWriter, r *http.Request, lang string) {
	http.SetCookie(w, &http.Cookie{Name: langCookie, Value: lang, Path: "/", MaxAge: 365 * 24 * 3600,
		HttpOnly: true, Secure: s.secureCookies(r), SameSite: http.SameSiteLaxMode})
}

// langName is a language's name in that language (never translated).
func langName(l string) string {
	if l == "nl" {
		return "Nederlands"
	}
	return "English"
}
