// Package github fetches the numbers that grow a harvest tree.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"sync"
	"time"
)

// Stats is everything the renderer needs to know about a developer.
type Stats struct {
	Login         string
	Name          string
	Repos         int
	Stars         int
	Followers     int
	Contributions int // last 12 months; -1 when unknown (no token)
	PullRequests  int
	AccountYears  float64
	TopLanguage   string
}

// Demo returns plausible fixed stats for previews and tests.
func Demo() Stats {
	return Stats{Login: "octocat", Name: "The Octocat", Repos: 38, Stars: 214,
		Followers: 87, Contributions: 1432, PullRequests: 96, AccountYears: 6.4, TopLanguage: "Go"}
}

var loginRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)

// ValidLogin reports whether s looks like a GitHub username.
func ValidLogin(s string) bool { return loginRe.MatchString(s) }

// ErrNotFound is returned when the user does not exist.
var ErrNotFound = errors.New("github: user not found")

// Client talks to the GitHub REST and GraphQL APIs.
type Client struct {
	Token string // optional personal/app token; unlocks contribution counts
	HTTP  *http.Client

	mu    sync.Mutex
	cache map[string]cached
	TTL   time.Duration
}

type cached struct {
	s       Stats
	expires time.Time
}

// New builds a Client with a one hour cache.
func New(token string) *Client {
	return &Client{Token: token, HTTP: &http.Client{Timeout: 15 * time.Second},
		cache: map[string]cached{}, TTL: time.Hour}
}

// Stats returns (cached) stats for a login.
func (c *Client) Stats(ctx context.Context, login string) (Stats, error) {
	if !ValidLogin(login) {
		return Stats{}, ErrNotFound
	}
	c.mu.Lock()
	if e, ok := c.cache[login]; ok && time.Now().Before(e.expires) {
		c.mu.Unlock()
		return e.s, nil
	}
	c.mu.Unlock()

	s, err := c.fetch(ctx, login)
	if err != nil {
		return Stats{}, err
	}
	c.mu.Lock()
	c.cache[login] = cached{s, time.Now().Add(c.TTL)}
	c.mu.Unlock()
	return s, nil
}

func (c *Client) do(ctx context.Context, method, u string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "harvest-tree")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode >= 400:
		return fmt.Errorf("github: %s %s: %s", method, u, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) fetch(ctx context.Context, login string) (Stats, error) {
	var u struct {
		Login       string    `json:"login"`
		Name        string    `json:"name"`
		Followers   int       `json:"followers"`
		PublicRepos int       `json:"public_repos"`
		CreatedAt   time.Time `json:"created_at"`
	}
	if err := c.do(ctx, "GET", "https://api.github.com/users/"+url.PathEscape(login), nil, &u); err != nil {
		return Stats{}, err
	}
	s := Stats{Login: u.Login, Name: u.Name, Followers: u.Followers, Repos: u.PublicRepos,
		AccountYears: time.Since(u.CreatedAt).Hours() / 24 / 365, Contributions: -1}

	langs := map[string]int{}
	for page := 1; page <= 5; page++ { // up to 500 repos
		var repos []struct {
			Stars    int    `json:"stargazers_count"`
			Language string `json:"language"`
			Fork     bool   `json:"fork"`
		}
		q := fmt.Sprintf("https://api.github.com/users/%s/repos?per_page=100&type=owner&page=%d", url.PathEscape(login), page)
		if err := c.do(ctx, "GET", q, nil, &repos); err != nil {
			return Stats{}, err
		}
		for _, r := range repos {
			if r.Fork {
				continue
			}
			s.Stars += r.Stars
			if r.Language != "" {
				langs[r.Language]++
			}
		}
		if len(repos) < 100 {
			break
		}
	}
	type kv struct {
		k string
		v int
	}
	var ls []kv
	for k, v := range langs {
		ls = append(ls, kv{k, v})
	}
	sort.Slice(ls, func(i, j int) bool { return ls[i].v > ls[j].v || ls[i].v == ls[j].v && ls[i].k < ls[j].k })
	if len(ls) > 0 {
		s.TopLanguage = ls[0].k
	}

	if c.Token != "" {
		c.contributions(ctx, &s) // best effort
	}
	return s, nil
}

func (c *Client) contributions(ctx context.Context, s *Stats) {
	const q = `query($login:String!){user(login:$login){
	  pullRequests{totalCount}
	  contributionsCollection{contributionCalendar{totalContributions}}}}`
	body, _ := json.Marshal(map[string]any{"query": q, "variables": map[string]string{"login": s.Login}})
	var out struct {
		Data struct {
			User struct {
				PullRequests struct{ TotalCount int } `json:"pullRequests"`
				CC           struct {
					Cal struct {
						Total int `json:"totalContributions"`
					} `json:"contributionCalendar"`
				} `json:"contributionsCollection"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := c.do(ctx, "POST", "https://api.github.com/graphql", body, &out); err != nil {
		return
	}
	s.Contributions = out.Data.User.CC.Cal.Total
	s.PullRequests = out.Data.User.PullRequests.TotalCount
}
