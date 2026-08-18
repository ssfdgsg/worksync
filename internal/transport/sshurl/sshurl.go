// Package sshurl parses and canonicalizes ssh:// store URLs (design §16.2).
//
// The parser must distinguish absolute paths from user-home-relative paths
// without relying on remote shell expansion of "~".
package sshurl

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// URL is a parsed ssh:// store location.
type URL struct {
	User string // "" means default user (current user on remote)
	Host string
	Port string // "" means default ssh port 22
	Path string // either "/absolute/path" or "~/home-relative-path"
}

// Scheme is the only transport scheme supported in v0.
const Scheme = "ssh"

// Parse parses an ssh URL of the forms:
//
//	ssh://user@host/absolute/path
//	ssh://user@host/~/home-relative-path
//	ssh://user@host:2222/absolute/path
func Parse(raw string) (*URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse ssh url: %w", err)
	}
	if u.Scheme != Scheme {
		return nil, fmt.Errorf("unsupported scheme %q (want %q)", u.Scheme, Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("ssh url requires a host")
	}
	if strings.Contains(u.Host, "@") {
		return nil, fmt.Errorf("malformed ssh url host %q", u.Host)
	}
	if u.User != nil {
		// Reject password-in-URL: design §20.1 never keeps plaintext creds.
		if _, hasPw := u.User.Password(); hasPw {
			return nil, fmt.Errorf("ssh url must not contain a password")
		}
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("ssh url must not contain query or fragment")
	}
	hostname := u.Host
	port := ""
	if h, p, err := net.SplitHostPort(u.Host); err == nil {
		hostname = h
		port = p
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("invalid ssh port %q", p)
		}
	} else if strings.Count(u.Host, ":") > 1 {
		return nil, fmt.Errorf("invalid ssh host %q (IPv6 addresses require an explicit port)", u.Host)
	}
	if hostname == "" {
		return nil, fmt.Errorf("ssh url requires a host")
	}
	path := u.Path
	if path == "" || path == "/" {
		return nil, fmt.Errorf("ssh url requires a store path")
	}
	if strings.HasPrefix(path, "/~") {
		// "ssh://user@host/~/x" -> home-relative "~/x"
		path = path[1:] // strip leading "/"
	} else if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("ssh url path must be absolute or ~-relative")
	}
	user := ""
	if u.User != nil {
		user = u.User.Username()
	}
	return &URL{User: user, Host: hostname, Port: port, Path: path}, nil
}

// IsHomeRelative reports whether Path is relative to the remote user's home
// ("~/...").
func (u *URL) IsHomeRelative() bool { return strings.HasPrefix(u.Path, "~/") }

// AbsolutePath returns the concrete remote path, resolving "~" against home.
// home is the remote user's home directory, known without shell expansion
// (e.g. from the remote user database).
func (u *URL) AbsolutePath(home string) string {
	if u.IsHomeRelative() {
		return strings.TrimSuffix(home, "/") + "/" + strings.TrimPrefix(u.Path, "~/")
	}
	return u.Path
}

// HostPort returns "host:port", using port 22 when none was given.
func (u *URL) HostPort() string {
	if u.Port != "" {
		return net.JoinHostPort(u.Host, u.Port)
	}
	return u.Host
}

// String re-serializes the URL canonically (never containing a password).
func (u *URL) String() string {
	var b strings.Builder
	b.WriteString(Scheme)
	b.WriteString("://")
	if u.User != "" {
		b.WriteString(u.User)
		b.WriteString("@")
	}
	b.WriteString(u.Host)
	if u.Port != "" {
		b.WriteString(":")
		b.WriteString(u.Port)
	}
	if u.IsHomeRelative() {
		b.WriteString("/")
	}
	b.WriteString(u.Path)
	return b.String()
}
