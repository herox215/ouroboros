package history

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pageArchive(t *testing.T, count, bytes int) *Archive {
	t.Helper()
	a := Open(t.TempDir())
	path, err := a.path("owner/page")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	var data strings.Builder
	data.WriteString("invalid JSON\n{\"seq\":0}\n")
	for i := 0; i < count; i++ {
		b, err := json.Marshal(Record{Seq: 100 + i*3, Content: strings.Repeat("x", bytes), Role: "user"})
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(&data, string(b))
	}
	if err := os.WriteFile(path, []byte(data.String()), 0600); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestPageBoundaries(t *testing.T) {
	for _, count := range []int{0, 1, 39, 40, 41, 80, 81} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			a := pageArchive(t, count, 0)
			for _, after := range []int{0, 1, 39, 40, 80, 100} {
				p, err := a.Page("owner/page", after)
				n := count - after
				if n < 0 {
					n = 0
				}
				if n > 40 {
					n = 40
				}
				if err != nil || p.Records == nil || len(p.Records) != n || p.Next != after+n || p.More != (count > after+n) {
					t.Fatalf("after %d: %+v, %v", after, p, err)
				}
				if n > 0 && p.Records[0].Seq != 100+after*3 {
					t.Fatal("offset used sequence number")
				}
			}
		})
	}
}

func TestPageByteBound(t *testing.T) {
	for _, bytes := range []int{60000, 128*1024 + 1} {
		a := pageArchive(t, 3, bytes)
		first, err := a.Page("owner/page", 0)
		want := 2
		if bytes > 128*1024 {
			want = 1
		}
		if err != nil || len(first.Records) != want || first.Next != want || !first.More {
			t.Fatalf("first: %+v, %v", first, err)
		}
		last, err := a.Page("owner/page", 2)
		if err != nil || len(last.Records) != 1 || last.Next != 3 || last.More {
			t.Fatalf("last: %+v, %v", last, err)
		}
	}
}

func TestPageErrors(t *testing.T) {
	a := pageArchive(t, 1, 0)
	if _, err := a.Page("owner/page", -1); err == nil {
		t.Fatal("negative offset")
	}
	for _, id := range []string{"", "page", "../page", "owner/../../page", "peer-bad/page", "owner/missing", "peer-0123456789abcdef0123456789abcdef/missing"} {
		_, want := a.Records(id)
		_, got := a.Page(id, 0)
		if want == nil || got == nil || want.Error() != got.Error() {
			t.Fatalf("%q: %v != %v", id, got, want)
		}
	}
	if _, err := a.Page("owner/missing", 0); !errors.Is(err, ErrNotArchived) {
		t.Fatal(err)
	}
}
