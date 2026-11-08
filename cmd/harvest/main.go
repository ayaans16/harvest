// harvest grows a cute animated voxel tree from your GitHub stats.
//
//	harvest serve              run the web service (see README for env vars)
//	harvest render -user NAME  write NAME.gif once (handy for GitHub Actions)
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"github.com/ayaans16/harvest/internal/github"
	"github.com/ayaans16/harvest/internal/render"
	"github.com/ayaans16/harvest/internal/server"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: harvest serve | harvest render -user NAME [-out FILE]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		serve()
	case "render":
		renderOnce(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, "unknown command:", os.Args[1])
		os.Exit(2)
	}
}

func renderOnce(args []string) {
	fs := flag.NewFlagSet("render", flag.ExitOnError)
	user := fs.String("user", "", "GitHub username (or \"demo\")")
	out := fs.String("out", "", "output file (default <user>.gif)")
	fs.Parse(args)
	if *user == "" {
		log.Fatal("-user is required")
	}
	var st github.Stats
	if *user == "demo" {
		st = github.Demo()
	} else {
		var err error
		st, err = github.New(os.Getenv("GITHUB_TOKEN")).Stats(context.Background(), *user)
		if err != nil {
			log.Fatal(err)
		}
	}
	if *out == "" {
		*out = *user + ".gif"
	}
	b, err := server.Encode(render.Render(st))
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote %s (%d KB)\n", *out, len(b)/1024)
}

func serve() {
	domain := os.Getenv("HARVEST_DOMAIN") // set in production for automatic HTTPS
	base := os.Getenv("HARVEST_BASE_URL")
	if base == "" && domain != "" {
		base = "https://" + domain
	}
	s := server.New(server.Config{
		BaseURL:      base,
		Token:        os.Getenv("GITHUB_TOKEN"),
		ClientID:     os.Getenv("GITHUB_CLIENT_ID"),
		ClientSecret: os.Getenv("GITHUB_CLIENT_SECRET"),
	})
	if _, err := url.Parse(base); err != nil {
		log.Fatal("bad HARVEST_BASE_URL: ", err)
	}

	if domain == "" { // local development
		addr := os.Getenv("HARVEST_ADDR")
		if addr == "" {
			addr = ":8080"
		}
		log.Println("listening on", addr)
		log.Fatal(srv(addr, s.Handler()).ListenAndServe())
	}

	// Production: Let's Encrypt certificates straight from Go, no reverse proxy.
	m := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(domain),
		Cache:      autocert.DirCache(envOr("HARVEST_CERT_DIR", "/var/lib/harvest/certs")),
	}
	go func() { // port 80: ACME challenges + redirect to https
		log.Fatal(srv(":80", m.HTTPHandler(nil)).ListenAndServe())
	}()
	hs := srv(":443", s.Handler())
	hs.TLSConfig = &tls.Config{GetCertificate: m.GetCertificate, MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1", "acme-tls/1"}}
	log.Println("serving https://" + domain)
	log.Fatal(hs.ListenAndServeTLS("", ""))
}

func srv(addr string, h http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 2 * time.Minute}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
