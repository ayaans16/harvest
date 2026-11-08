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
<title>harvest - grow a tree from your GitHub</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link href="https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@400;700&family=Silkscreen:wght@400;700&display=swap" rel="stylesheet">
<style>
:root{--bg:#0e1813;--bg2:#1c3727;--panel:#13241d;--edge:#2f5242;--ink:#e9f1dc;--soft:#8fae9c;--lime:#b8f55a;--limeink:#10201a;--shadow:#0a110e}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;background:linear-gradient(var(--bg),var(--bg2));color:var(--ink);font:15px/1.6 "JetBrains Mono",ui-monospace,Menlo,monospace}
.wrap{max-width:1100px;margin:0 auto;padding:40px 16px;display:grid;gap:28px;grid-template-columns:minmax(0,5fr) minmax(0,6fr);align-items:center}
.panel{background:var(--panel);border:3px solid var(--edge);box-shadow:6px 6px 0 var(--shadow);padding:28px}
.tag{display:inline-block;background:var(--lime);color:var(--limeink);font:700 12px "Silkscreen",monospace;letter-spacing:.2em;padding:6px 10px;text-transform:uppercase}
h1{font:700 clamp(22px,3.4vw,34px)/1.25 "Silkscreen",monospace;text-transform:uppercase;margin:22px 0 14px;text-shadow:3px 3px 0 var(--edge)}
p{color:var(--soft);margin:0 0 20px}
form{display:flex;gap:10px;flex-wrap:wrap}
input{flex:1 1 180px;min-width:0;padding:12px 14px;background:var(--bg);border:3px solid var(--edge);color:var(--ink);font:inherit;border-radius:0}
input:focus{outline:none;border-color:var(--lime)}
button,.btn{padding:12px 18px;background:var(--lime);color:var(--limeink);border:0;box-shadow:4px 4px 0 var(--shadow);font:700 15px "JetBrains Mono",monospace;cursor:pointer;text-decoration:none;border-radius:0}
button:active,.btn:active{transform:translate(2px,2px);box-shadow:2px 2px 0 var(--shadow)}
.ghost{background:transparent;color:var(--ink);border:3px solid var(--edge)}
.row{display:flex;gap:10px;flex-wrap:wrap;margin-top:12px}
.art{border:3px solid var(--edge);box-shadow:6px 6px 0 var(--shadow);background:var(--panel);line-height:0}
.art img{width:100%;height:auto;image-rendering:pixelated}
.snip{margin-top:22px;border:3px dashed var(--edge);padding:12px;font-size:12.5px;color:var(--soft);overflow:auto;white-space:pre;user-select:all}
.snip b{display:block;color:var(--lime);font:700 11px "Silkscreen",monospace;letter-spacing:.15em;margin-bottom:6px;text-transform:uppercase;user-select:none}
@media(max-width:820px){.wrap{grid-template-columns:1fr}.art{order:-1}}
</style></head><body><div class="wrap">
<section class="panel">
<span class="tag">github stats, but a tree</span>
<h1>Every commit grows a leaf.</h1>
<p>Type a GitHub username and get a tiny animated voxel tree built from stars, followers, repos and contributions. Drop it in your profile README.</p>
<form onsubmit="location.href='/?user='+encodeURIComponent(this.u.value);return false">
<input name="u" placeholder="github username" value="{{.User}}" autocomplete="off" spellcheck="false" aria-label="GitHub username">
<button>Grow it</button>
</form>
{{if .SignIn}}<div class="row"><a class="btn ghost" href="/login">Sign in with GitHub</a></div>{{end}}
{{if .User}}<div class="snip"><b>Paste into your README</b>![{{.User}}'s harvest tree]({{.Base}}/{{.User}}.gif)</div>{{end}}
</section>
<div class="art"><img src="/{{if .User}}{{.User}}{{else}}demo{{end}}.gif" alt="animated voxel tree" width="660" height="384"></div>
</div></body></html>`))

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
