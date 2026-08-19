package cli

import (
	"testing"
)

func TestLoadedImageRef(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Loaded image: docker.io/library/busybox:latest\n", "docker.io/library/busybox:latest"},
		{"Loaded image(s): sha256:abc\n", "sha256:abc"},
		{"some other output\n", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := loadedImageRef(c.in); got != c.want {
			t.Errorf("loadedImageRef(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
