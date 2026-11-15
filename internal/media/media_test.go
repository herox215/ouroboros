package media

import "testing"

func TestKind(t *testing.T) {
	for _, c := range []struct {
		data []byte
		kind string
		ext  string
	}{
		{[]byte("\x89PNG\r\n\x1a\n0000"), "image/png", ".png"},
		{[]byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00"), "image/jpeg", ".jpg"},
		{[]byte("GIF89a\x00\x00"), "image/gif", ".gif"},
		{[]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), "image/webp", ".webp"},
		{[]byte("<html>not a picture</html>"), "", ""},
		{nil, "", ""},
	} {
		if got := Kind(c.data); got != c.kind {
			t.Fatalf("%q is %q, not %q", c.data, got, c.kind)
		}
		if got := Ext(c.kind); got != c.ext {
			t.Fatalf("the extension of %q is %q, not %q", c.kind, got, c.ext)
		}
		if got := KindOfName("name" + c.ext); got != c.kind {
			t.Fatalf("name%s is %q, not %q", c.ext, got, c.kind)
		}
	}
	if IsPicture("image/svg+xml") || IsPicture("") {
		t.Fatal("what is no picture is taken for one")
	}
	if KindOfName("notes.txt") != "" || KindOfName("A.JPEG") != "image/jpeg" {
		t.Fatal("a name is not read as a picture")
	}
}
