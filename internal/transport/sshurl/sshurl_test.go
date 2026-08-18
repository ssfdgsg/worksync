package sshurl

import "testing"

func TestParseAbsolute(t *testing.T) {
	u, err := Parse("ssh://user@example.com/var/worksync-store")
	if err != nil {
		t.Fatal(err)
	}
	if u.User != "user" || u.Host != "example.com" || u.Port != "" {
		t.Errorf("got %+v", u)
	}
	if u.Path != "/var/worksync-store" || u.IsHomeRelative() {
		t.Errorf("path = %q", u.Path)
	}
	if got := u.AbsolutePath("/home/user"); got != "/var/worksync-store" {
		t.Errorf("AbsolutePath = %q", got)
	}
}

func TestParseHomeRelative(t *testing.T) {
	u, err := Parse("ssh://user@example.com/~/worksync-store")
	if err != nil {
		t.Fatal(err)
	}
	if !u.IsHomeRelative() {
		t.Errorf("expected home-relative, got %+v", u)
	}
	if u.Path != "~/worksync-store" {
		t.Errorf("path = %q", u.Path)
	}
	if got := u.AbsolutePath("/home/user"); got != "/home/user/worksync-store" {
		t.Errorf("AbsolutePath = %q", got)
	}
}

func TestParseWithPort(t *testing.T) {
	u, err := Parse("ssh://user@example.com:2222/abs/path")
	if err != nil {
		t.Fatal(err)
	}
	if u.Port != "2222" {
		t.Errorf("port = %q", u.Port)
	}
	if u.HostPort() != "example.com:2222" {
		t.Errorf("HostPort = %q", u.HostPort())
	}
	if s := u.String(); s != "ssh://user@example.com:2222/abs/path" {
		t.Errorf("String = %q", s)
	}
}

func TestParseNoUser(t *testing.T) {
	u, err := Parse("ssh://example.com/srv")
	if err != nil {
		t.Fatal(err)
	}
	if u.User != "" || u.Host != "example.com" {
		t.Errorf("got %+v", u)
	}
	if u.HostPort() != "example.com" {
		t.Errorf("HostPort = %q", u.HostPort())
	}
}

func TestParseRejectsPassword(t *testing.T) {
	if _, err := Parse("ssh://user:secret@example.com/store"); err == nil {
		t.Fatal("expected password rejection")
	}
}

func TestParseRejectsBadScheme(t *testing.T) {
	if _, err := Parse("https://example.com/store"); err == nil {
		t.Fatal("expected scheme rejection")
	}
}

func TestParseRejectsNoPath(t *testing.T) {
	if _, err := Parse("ssh://user@example.com"); err == nil {
		t.Fatal("expected missing-path rejection")
	}
	if _, err := Parse("ssh://user@example.com/"); err == nil {
		t.Fatal("expected missing-path rejection")
	}
}

func TestParseRejectsBadPort(t *testing.T) {
	if _, err := Parse("ssh://user@example.com:99999/store"); err == nil {
		t.Fatal("expected bad-port rejection")
	}
}

func TestParseRejectsQueryFragment(t *testing.T) {
	if _, err := Parse("ssh://user@example.com/store?x=1"); err == nil {
		t.Fatal("expected query rejection")
	}
	if _, err := Parse("ssh://user@example.com/store#frag"); err == nil {
		t.Fatal("expected fragment rejection")
	}
}

func TestStringRoundTrip(t *testing.T) {
	cases := []string{
		"ssh://user@example.com/var/worksync-store",
		"ssh://user@example.com/~/worksync-store",
		"ssh://user@example.com:2222/abs/path",
		"ssh://example.com/srv",
	}
	for _, c := range cases {
		u, err := Parse(c)
		if err != nil {
			t.Fatalf("%s: %v", c, err)
		}
		if got := u.String(); got != c {
			t.Errorf("%s -> String = %q", c, got)
		}
	}
}
