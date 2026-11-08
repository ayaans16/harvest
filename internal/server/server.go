// Package server exposes the tree over HTTP and handles "Sign in with GitHub".
package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"image/gif"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ayaans16/harvest/internal/github"
	"github.com/ayaans16/harvest/internal/render"
)

// Config holds the runtime settings, all supplied through the environment.
type Config struct {
	BaseURL      string // e.g. https://harvest.ayaanshaikh.ca
	Token        string // optional GitHub token for contribution counts
	ClientID     string // GitHub OAuth / GitHub App client id
	ClientSecret string
}

type Server struct {
	cfg Config
	gh  *github.Client

	mu    sync.Mutex
	cache map[string]cachedGIF
}

type cachedGIF struct {
	body    []byte
	expires time.Time
}

func New(cfg Config) *Server {
	return &Server{cfg: cfg, gh: github.New(cfg.Token), cache: map[string]cachedGIF{}}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /login", s.login)
	mux.HandleFunc("GET /callback", s.callback)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /{user}", s.tree)
	return mux
}

// Encode renders a GIF to bytes.
func Encode(g *gif.GIF) ([]byte, error) {
	var b bytes.Buffer
	err := gif.EncodeAll(&b, g)
	return b.Bytes(), err
}

func (s *Server) tree(w http.ResponseWriter, r *http.Request) {
	user := strings.TrimSuffix(r.PathValue("user"), ".gif")
	var st github.Stats
	if user == "demo" {
		st = github.Demo()
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		var err error
		if st, err = s.gh.Stats(ctx, user); err != nil {
			if errors.Is(err, github.ErrNotFound) {
				http.Error(w, "no such GitHub user", http.StatusNotFound)
			} else {
				http.Error(w, "GitHub is unreachable, try again soon", http.StatusBadGateway)
			}
			return
		}
	}

	s.mu.Lock()
	c, ok := s.cache[st.Login]
	s.mu.Unlock()
	if !ok || time.Now().After(c.expires) {
		body, err := Encode(render.Render(st))
		if err != nil {
			http.Error(w, "render failed", http.StatusInternalServerError)
			return
		}
		c = cachedGIF{body, time.Now().Add(time.Hour)}
		s.mu.Lock()
		if len(s.cache) > 500 {
			s.cache = map[string]cachedGIF{}
		}
		s.cache[st.Login] = c
		s.mu.Unlock()
	}
	w.Header().Set("Content-Type", "image/gif")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write(c.body)
}

// ---- GitHub OAuth ----------------------------------------------------

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ClientID == "" {
		http.Error(w, "GitHub sign-in is not configured", http.StatusServiceUnavailable)
		return
	}
	b := make([]byte, 16)
	rand.Read(b)
	state := hex.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{Name: "state", Value: state, Path: "/", MaxAge: 600,
		HttpOnly: true, Secure: strings.HasPrefix(s.cfg.BaseURL, "https"), SameSite: http.SameSiteLaxMode})
	q := url.Values{"client_id": {s.cfg.ClientID}, "state": {state},
		"redirect_uri": {s.cfg.BaseURL + "/callback"}}
	http.Redirect(w, r, "https://github.com/login/oauth/authorize?"+q.Encode(), http.StatusFound)
}

func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	ck, err := r.Cookie("state")
	if err != nil || ck.Value == "" || ck.Value != r.FormValue("state") {
		http.Error(w, "bad state", http.StatusBadRequest)
		return
	}
	form := url.Values{"client_id": {s.cfg.ClientID}, "client_secret": {s.cfg.ClientSecret},
		"code": {r.FormValue("code")}, "redirect_uri": {s.cfg.BaseURL + "/callback"}}
	req, _ := http.NewRequestWithContext(r.Context(), "POST", "https://github.com/login/oauth/access_token",
		strings.NewReader(form.Encode()))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, "GitHub is unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if json.NewDecoder(resp.Body).Decode(&tok) != nil || tok.AccessToken == "" {
		http.Error(w, "sign-in failed", http.StatusUnauthorized)
		return
	}
	// Ask GitHub who just signed in. The token is used once and never stored.
	ureq, _ := http.NewRequestWithContext(r.Context(), "GET", "https://api.github.com/user", nil)
	ureq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	ureq.Header.Set("User-Agent", "harvest-tree")
	uresp, err := http.DefaultClient.Do(ureq)
	if err != nil {
		http.Error(w, "GitHub is unreachable", http.StatusBadGateway)
		return
	}
	defer uresp.Body.Close()
	var u struct {
		Login string `json:"login"`
	}
	if json.NewDecoder(uresp.Body).Decode(&u) != nil || !github.ValidLogin(u.Login) {
		http.Error(w, "sign-in failed", http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, "/?user="+url.QueryEscape(u.Login), http.StatusFound)
}

// ---- landing page ----------------------------------------------------

var page = template.Must(template.New("home").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>harvest · a tree that grows from your GitHub</title>
<style>
:root{--bg:#f6fcff;--ink:#3d3357;--soft:#7a6f99;--card:#fffaf3;--edge:#f0d9c8;--btn:#57c466}
@media(prefers-color-scheme:dark){:root{--bg:#1d1a2b;--ink:#f1ecff;--soft:#a79fc7;--card:#2a2640;--edge:#3b3560}}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px 16px;background:var(--bg);color:var(--ink);font:16px/1.5 ui-rounded,"SF Pro Rounded",system-ui,sans-serif}
main{width:100%;max-width:680px;text-align:center}
img{width:100%;height:auto;image-rendering:pixelated;border-radius:20px;border:3px solid var(--edge);background:var(--card)}
h1{margin:.2em 0 0;font-size:2rem}p{color:var(--soft);margin:.3em 0 1em}
form{display:flex;gap:8px;justify-content:center;flex-wrap:wrap}
input{padding:10px 14px;border-radius:12px;border:2px solid var(--edge);background:var(--card);color:var(--ink);font:inherit;min-width:0;flex:1 1 200px;max-width:260px}
button,.btn{padding:10px 18px;border-radius:12px;border:0;background:var(--btn);color:#fff;font:inherit;font-weight:700;cursor:pointer;text-decoration:none}
.alt{background:#7a6f99}
pre{text-align:left;overflow:auto;background:var(--card);border:2px solid var(--edge);border-radius:12px;padding:12px;font-size:.85rem;user-select:all}
</style></head><body><main>
<img src="/{{if .User}}{{.User}}{{else}}demo{{end}}.gif" alt="your harvest tree" width="660" height="384">
<h1>harvest 🌳</h1>
<p>A little voxel tree that grows from your GitHub stats.</p>
<form onsubmit="location.href='/?user='+encodeURIComponent(this.u.value);return false">
<input name="u" placeholder="github username" value="{{.User}}" autocomplete="off" spellcheck="false">
<button>Grow</button>{{if .SignIn}}<a class="btn alt" href="/login">Sign in with GitHub</a>{{end}}
</form>
{{if .User}}<p>Put it in your profile README:</p>
<pre>&lt;img src="{{.Base}}/{{.User}}.gif" alt="{{.User}}'s harvest tree" width="660"&gt;</pre>{{end}}
</main></body></html>`))

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	user := r.URL.Query().Get("user")
	if !github.ValidLogin(user) {
		user = ""
	}
	base := s.cfg.BaseURL
	if base == "" {
		base = "http://" + r.Host
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := page.Execute(w, map[string]any{"User": user, "Base": base, "SignIn": s.cfg.ClientID != ""}); err != nil {
		fmt.Println("home:", err)
	}
}
