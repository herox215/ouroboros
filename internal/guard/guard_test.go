package guard

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var errTest = errors.New("test error")

// world is a machine with a user, a node and a guard for it.
type world struct {
	t         *testing.T
	root      string // everything is below it
	home      string // of the user
	node      string // the directory of the node
	guard     *Guard
	protected []string
	failing   error // what reading the configuration fails with
}

func newWorld(t *testing.T) *world {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, root: root, home: filepath.Join(root, "home"), protected: []string{"~/.ssh"}}
	w.node = filepath.Join(w.home, ".config", "ouroboros")
	t.Setenv("HOME", w.home)
	w.protected = []string{"~/.ssh"}
	w.file(w.node+"/config.json", "{}")
	w.file(w.node+"/config.json.lock", "")
	w.file(w.node+"/contacts.json", "{}")
	w.file(w.home+"/.ssh/id_ed25519", "key")
	w.file(w.home+"/notes.md", "notes")
	w.file(w.home+"/share/hello.txt", "hello")
	w.guard = &Guard{
		Home:      w.node,
		Secrets:   []string{w.node + "/config.json", w.node + "/config.json.lock"},
		Protected: func() ([]string, error) { return w.protected, w.failing },
	}
	return w
}

// file writes a file with its directories.
func (w *world) file(path, content string) {
	w.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

// link makes name a symbolic link to target.
func (w *world) link(target, name string) {
	w.t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.Symlink(target, name); err != nil {
		w.t.Fatal(err)
	}
}

// check asks the guard about one path.
func (w *world) check(a Access, op Op, path string) (string, error) {
	w.t.Helper()
	c, err := w.guard.Checker(a)
	if err != nil {
		w.t.Fatalf("checker: %v", err)
	}
	return c.Check(op, path)
}

// allowed fails the test unless the tool may do op with path, and works on real.
func (w *world) allowed(a Access, op Op, path, real string) {
	w.t.Helper()
	if got, err := w.check(a, op, path); err != nil || got != real {
		w.t.Errorf("%v %s = %q, %v; want %q", op, path, got, err, real)
	}
}

// refused fails the test unless the tool is refused op with path, by rule.
func (w *world) refused(a Access, op Op, path, rule string) {
	w.t.Helper()
	got, err := w.check(a, op, path)
	var refusal *Refusal
	if !errors.As(err, &refusal) || got != "" || refusal.Rule != rule || refusal.Op != op || refusal.Path != path {
		w.t.Errorf("%v %s = %q, %v; want the refusal %q", op, path, got, err, rule)
	}
}

func paths(p ...string) *[]string { return &p }

const (
	ruleSecret = "it holds the secrets of this node, which no tool reads or changes"
	ruleState  = "it is in the directory of this node, where the node keeps its state: no tool changes it"
	ruleRoot   = "the root of the file system and the home directory are never deleted"

	ruleToolchain = "it is part of the Go that rebuild builds ouroboros with, which no tool changes"
	ruleSSH       = `~/.ssh is in "protected" of the configuration, which no tool touches`
)

// For the owner a tool without settings goes everywhere, for a peer nowhere.
func TestWithoutSettings(t *testing.T) {
	w := newWorld(t)
	owner, peer := Access{Tool: "read"}, Access{Tool: "read", Peer: true}
	for _, op := range []Op{Read, Write, Delete} {
		w.allowed(owner, op, "~/notes.md", w.home+"/notes.md")
		w.allowed(owner, op, w.root+"/new/file", w.root+"/new/file")
		w.refused(peer, op, "~/notes.md", `the tool read has no "allow" for peers, so it reaches nothing`)
	}
	w.allowed(owner, Read, "~", w.home)
	w.allowed(owner, Read, "/", "/")
}

// A path is a file, or a directory with everything below it; deny wins over
// allow.
func TestAllowAndDeny(t *testing.T) {
	w := newWorld(t)
	w.file(w.home+"/share2/x", "")
	w.file(w.home+"/share/inbox/keep/x", "")
	for _, peer := range []bool{false, true} {
		a := Access{Tool: "write", Allow: paths("~/share", w.home+"/notes.md"), Deny: []string{"~/share/inbox/keep"}, Peer: peer}
		w.allowed(a, Read, "~/share", w.home+"/share")
		w.allowed(a, Read, "~/share/hello.txt", w.home+"/share/hello.txt")
		w.allowed(a, Write, "~/share/inbox/new.txt", w.home+"/share/inbox/new.txt")
		w.allowed(a, Read, "~/notes.md", w.home+"/notes.md")
		w.refused(a, Read, "~/share2/x", `it is not in "allow" of the tool write`)
		w.refused(a, Read, "~/notes.md.bak", `it is not in "allow" of the tool write`)
		w.refused(a, Read, "~", `it is not in "allow" of the tool write`)
		w.refused(a, Write, "~/share/inbox/keep", `~/share/inbox/keep is in "deny" of the tool write`)
		w.refused(a, Write, "~/share/inbox/keep/x", `~/share/inbox/keep is in "deny" of the tool write`)
		w.allowed(a, Write, "~/share/inbox/keeper", w.home+"/share/inbox/keeper")

		// An empty list allows nothing, for the owner too.
		none := Access{Tool: "read", Allow: paths(), Peer: peer}
		w.refused(none, Read, "~/notes.md", `it is not in "allow" of the tool read`)
		// All of the file system has to be written out.
		all := Access{Tool: "read", Allow: paths("/"), Peer: peer}
		w.allowed(all, Read, "~/notes.md", w.home+"/notes.md")
		w.allowed(all, Read, "/", "/")
		// Denying without allowing: everything else, for the owner only.
		deny := Access{Tool: "read", Deny: []string{"~/share"}}
		w.allowed(deny, Read, "~/notes.md", w.home+"/notes.md")
		w.refused(deny, Read, "~/share/hello.txt", `~/share is in "deny" of the tool read`)
	}
}

// What is protected is touched by no tool, whatever it is allowed.
func TestProtected(t *testing.T) {
	w := newWorld(t)
	for _, a := range []Access{{Tool: "read"}, {Tool: "read", Allow: paths("/"), Peer: true}, {Tool: "read", Allow: paths("~/.ssh")}} {
		for _, op := range []Op{Read, Write, Delete} {
			w.refused(a, op, "~/.ssh", ruleSSH)
			w.refused(a, op, "~/.ssh/id_ed25519", ruleSSH)
			w.refused(a, op, "~/.ssh/new", ruleSSH)
		}
	}
	w.allowed(Access{Tool: "read"}, Read, "~/.sshx", w.home+"/.sshx")
	w.allowed(Access{Tool: "read"}, Read, "~/.ss", w.home+"/.ss")
	// Protected wins over deny as the rule that is named.
	w.refused(Access{Tool: "read", Deny: []string{"~/.ssh"}}, Read, "~/.ssh/id_ed25519", ruleSSH)

	// The owner may protect nothing.
	w.protected = nil
	w.allowed(Access{Tool: "read"}, Read, "~/.ssh/id_ed25519", w.home+"/.ssh/id_ed25519")

	// A protected place that is a link protects where it leads.
	w.file(w.root+"/vault/key", "key")
	w.link(w.root+"/vault", w.home+"/.keys")
	w.protected = []string{"~/.keys"}
	w.refused(Access{Tool: "read"}, Read, w.root+"/vault/key", `~/.keys is in "protected" of the configuration, which no tool touches`)
	w.refused(Access{Tool: "read"}, Read, "~/.keys/key", `~/.keys is in "protected" of the configuration, which no tool touches`)
}

// Without the rules no file is touched.
func TestRulesThatCannotBeRead(t *testing.T) {
	w := newWorld(t)
	w.failing = errTest
	if _, err := w.guard.Checker(Access{Tool: "read"}); !errors.Is(err, errTest) || !strings.Contains(err.Error(), "no file is touched") {
		t.Fatalf("a configuration that cannot be read: %v", err)
	}
	w.failing = nil

	for _, tt := range []struct {
		name      string
		protected []string
		access    Access
		want      string
	}{
		{"relative protected", []string{"secrets"}, Access{}, `"protected" in the configuration: the path "secrets" has to be absolute or start with ~`},
		{"protected below a file", []string{"~/notes.md/x"}, Access{}, `"protected" in the configuration: lstat ` + w.home + `/notes.md/x: not a directory`},
		{"relative allow", nil, Access{Allow: paths("share")}, `the path "share" has to be absolute or start with ~`},
		{"allow of another user", nil, Access{Allow: paths("~root/x")}, `the path "~root/x" has to be absolute or start with ~`},
		{"allow below a file", nil, Access{Allow: paths("~/notes.md/x")}, "lstat " + w.home + "/notes.md/x: not a directory"},
		{"relative deny", nil, Access{Deny: []string{"./x"}}, `the path "./x" has to be absolute or start with ~`},
	} {
		w.protected = tt.protected
		if c, err := w.guard.Checker(tt.access); err == nil || c != nil && tt.access.Deny == nil || err.Error() != tt.want {
			t.Errorf("%s: %v, want %q", tt.name, err, tt.want)
		}
	}
	w.protected = nil

	// Places of the node that cannot be looked at.
	g := *w.guard
	g.Home = w.home + "/notes.md/node"
	if _, err := g.Checker(Access{}); err == nil {
		t.Error("a directory of the node below a file")
	}
	g = *w.guard
	g.Secrets = []string{w.home + "/notes.md/config.json"}
	if _, err := g.Checker(Access{}); err == nil {
		t.Error("a secret below a file")
	}
	t.Setenv("HOME", w.home+"/notes.md/home")
	if _, err := w.guard.Checker(Access{}); err == nil {
		t.Error("a home directory below a file")
	}
}

// A path the tool is given is absolute, below "~" or, for the owner,
// relative to the workspace: the home directory without one. A peer names
// its paths in full.
func TestPathsOfTheTool(t *testing.T) {
	w := newWorld(t)
	a := Access{Tool: "read"}
	for path, want := range map[string]string{"notes.md": w.home + "/notes.md", "./share/hello.txt": w.home + "/share/hello.txt", "share/../notes.md": w.home + "/notes.md", ".": w.home} {
		if got, err := w.check(a, Read, path); err != nil || got != want {
			t.Errorf("%q: %q, %v; want %q", path, got, err, want)
		}
	}
	if got, err := w.check(a, Read, "../home/.ssh/id_ed25519"); err == nil || got != "" || !strings.Contains(err.Error(), `"protected"`) {
		t.Errorf("a relative path that leads to a rule: %q, %v", got, err)
	}
	for _, path := range []string{"", "~user/notes.md"} {
		if got, err := w.check(a, Read, path); err == nil || got != "" || err.Error() != `the path "`+path+`" has to be absolute or start with ~` {
			t.Errorf("%q: %q, %v", path, got, err)
		}
	}
	peer := Access{Tool: "read", Allow: &[]string{"/"}, Peer: true}
	if got, err := w.check(peer, Read, "notes.md"); err == nil || got != "" || err.Error() != `the path "notes.md" has to be absolute or start with ~: a peer names a path in full` {
		t.Errorf("a peer: %q, %v", got, err)
	}
	g := *w.guard
	g.Workspace = w.home + "/share"
	if c, err := g.Checker(a); err != nil {
		t.Fatal(err)
	} else if got, err := c.Check(Read, "hello.txt"); err != nil || got != w.home+"/share/hello.txt" {
		t.Errorf("in the workspace: %q, %v", got, err)
	}
	t.Setenv("HOME", "")
	w.protected = nil // "~/.ssh" needs the home directory too
	if c, err := w.guard.Checker(a); err != nil {
		t.Fatal(err)
	} else if got, err := c.Check(Read, "notes.md"); err == nil || got != "" || !strings.HasSuffix(err.Error(), "; the workspace a relative path starts from is not known") {
		t.Errorf("without a home directory: %q, %v", got, err)
	}
	t.Setenv("HOME", w.home)
	if got, err := w.check(a, Read, "~/notes.md/x"); err == nil || got != "" || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("below a file: %q, %v", got, err)
	}
	if err := Valid([]string{"~", "~/x", "/x"}); err != nil {
		t.Errorf("Valid: %v", err)
	}
	if err := Valid([]string{"/x", "x"}); err == nil || err.Error() != `the path "x" has to be absolute or start with ~` {
		t.Errorf("Valid: %v", err)
	}

	// Without a home directory "~" leads nowhere; everything else works.
	w.protected = nil
	t.Setenv("HOME", "")
	if got, err := w.check(a, Read, "~/notes.md"); err == nil || got != "" || !strings.HasPrefix(err.Error(), "the path ~/notes.md needs the home directory, which is not known: ") {
		t.Errorf("without a home directory: %q, %v", got, err)
	}
	w.protected = nil
	w.allowed(a, Delete, w.home+"/notes.md", w.home+"/notes.md")
	w.allowed(a, Delete, w.home, w.home)
}

// The secrets of the node are neither read nor changed, under any name.
func TestSecrets(t *testing.T) {
	w := newWorld(t)
	w.link(w.node+"/config.json", w.home+"/share/link")
	w.link("../.config/ouroboros", w.home+"/share/node")
	if err := os.Link(w.node+"/config.json", w.home+"/share/hard"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(w.node + "/config.json.lock"); err != nil {
		t.Fatal(err)
	}
	// A secret that is not there yet does not hide the one after it.
	w.guard.Secrets = []string{w.node + "/config.json.lock", w.node + "/config.json"}
	for _, a := range []Access{{Tool: "read"}, {Tool: "read", Allow: paths("/"), Peer: true}} {
		for _, path := range []string{
			"~/.config/ouroboros/config.json", "~/.config/ouroboros/config.json.lock",
			"~/share/link", "~/share/node/config.json", "~/share/hard",
			"~/share/../.config/./ouroboros//config.json",
		} {
			w.refused(a, Read, path, ruleSecret)
			w.refused(a, Write, path, ruleSecret)
		}
		// A link to it is a link: it can go.
		w.refused(a, Delete, "~/share/hard", ruleSecret)
		w.allowed(a, Delete, "~/share/link", w.home+"/share/link")
		// What is no secret is read, and nothing else.
		w.allowed(a, Read, "~/.config/ouroboros/contacts.json", w.node+"/contacts.json")
		w.allowed(a, Read, "~/share/node/contacts.json", w.node+"/contacts.json")
		w.allowed(a, Read, "~/share/missing", w.home+"/share/missing")
	}
}

// A secret that is a directory is one with everything below it: the
// directory of another profile of the node.
func TestSecretDirectories(t *testing.T) {
	w := newWorld(t)
	profile := w.node + "/profiles/0123456789ab"
	if err := os.MkdirAll(profile+"/history", 0o700); err != nil {
		t.Fatal(err)
	}
	w.link(profile, w.home+"/share/other")
	w.guard.Secrets = []string{profile}
	a := Access{Tool: "read"}
	for _, path := range []string{"~/.config/ouroboros/profiles/0123456789ab", "~/.config/ouroboros/profiles/0123456789ab/history/x.jsonl", "~/share/other/contacts.json"} {
		w.refused(a, Read, path, ruleSecret)
	}
	w.allowed(a, Read, "~/.config/ouroboros/profiles/0123456789abc", w.node+"/profiles/0123456789abc")
}

// The state of the node is read and never changed.
// The sockets the nodes are reached through are not changed, by any tool.
func TestSocketsOfTheNodes(t *testing.T) {
	w := newWorld(t)
	w.file(w.root+"/sockets/abc.sock", "")
	w.guard.Sockets = w.root + "/sockets"
	rule := "it holds the sockets the nodes are reached through: no tool changes it"
	w.refused(Access{Tool: "delete"}, Delete, w.root+"/sockets/abc.sock", rule)
	w.refused(Access{Tool: "write"}, Write, w.root+"/sockets/new", rule)
	w.allowed(Access{Tool: "read"}, Read, w.root+"/sockets/abc.sock", w.root+"/sockets/abc.sock")
	p, err := w.guard.Box(Access{Tool: "bash"}, false)
	if err != nil || !slices.Contains(p.ReadOnly, w.root+"/sockets") {
		t.Fatalf("the sandbox: %+v, %v", p, err)
	}
	// One that cannot be resolved touches nothing.
	w.guard.Sockets = w.root + "/loop"
	w.link("loop", w.root+"/loop")
	if _, err := w.guard.Checker(Access{Tool: "read"}); err == nil {
		t.Fatal("a directory of sockets that leads nowhere")
	}
}

func TestStateOfTheNode(t *testing.T) {
	w := newWorld(t)
	w.link(w.node, w.home+"/share/node")
	a := Access{Tool: "write"}
	for _, op := range []Op{Write, Delete} {
		w.refused(a, op, "~/.config/ouroboros", ruleState)
		w.refused(a, op, "~/.config/ouroboros/contacts.json", ruleState)
		w.refused(a, op, "~/.config/ouroboros/update/main.go", ruleState)
		// The log, also the part of it that was rotated, and its lock.
		w.refused(a, op, "~/.config/ouroboros/ouro.log", ruleState)
		w.refused(a, op, "~/.config/ouroboros/ouro.log.1", ruleState)
		w.refused(a, op, "~/.config/ouroboros/ouro.log.lock", ruleState)
		w.refused(a, op, "~/share/node/contacts.json", ruleState)
		w.allowed(a, op, "~/.config/ouroborosx", w.home+"/.config/ouroborosx")
		w.allowed(a, op, "~/.config", w.home+"/.config")
	}
	w.allowed(a, Read, "~/.config/ouroboros", w.node)
	w.allowed(a, Read, "~/.config/ouroboros/ouro.log.1", w.node+"/ouro.log.1")
	w.refused(a, Write, "~/share/node", ruleState)
	w.allowed(a, Delete, "~/share/node", w.home+"/share/node") // the link, not the node

	// The node may live behind a link.
	w.link(w.node, w.root+"/node")
	g := *w.guard
	g.Home = w.root + "/node"
	c, err := g.Checker(a)
	if err != nil {
		t.Fatal(err)
	}
	var refusal *Refusal
	if _, err := c.Check(Write, "~/.config/ouroboros/contacts.json"); !errors.As(err, &refusal) || refusal.Rule != ruleState {
		t.Errorf("a node behind a link: %v", err)
	}
}

// Neither the root of the file system nor the home directory is deleted.
func TestNeverDeleted(t *testing.T) {
	w := newWorld(t)
	w.link(w.home, w.root+"/me")
	a := Access{Tool: "delete", Allow: paths("/")}
	w.refused(a, Delete, "/", ruleRoot)
	w.refused(a, Delete, "~", ruleRoot)
	w.refused(a, Delete, "~/share/..", ruleRoot)
	w.refused(a, Delete, w.root+"/me/", ruleRoot)
	w.allowed(a, Delete, w.root+"/me", w.root+"/me")
	w.allowed(a, Write, "~", w.home)
	w.allowed(a, Read, "/", "/")

	// The home directory may be behind a link.
	t.Setenv("HOME", w.root+"/me")
	w.refused(a, Delete, w.home, ruleRoot)
}

// No link and no ".." leads around a rule: the path is checked where it
// leads.
func TestLinksAndDots(t *testing.T) {
	w := newWorld(t)
	w.link(w.home+"/.ssh", w.home+"/share/keys")             // absolute
	w.link("../../.ssh/id_ed25519", w.home+"/share/sub/key") // relative
	w.link(w.root+"/outside", w.home+"/share/out")           // to what is not there yet
	w.link("hello.txt", w.home+"/share/again")
	w.file(w.root+"/elsewhere/deep/x", "")
	w.link(w.root+"/elsewhere/deep", w.home+"/share/deep")
	a := Access{Tool: "read", Allow: paths("~/share"), Peer: true}

	w.refused(a, Read, "~/share/keys/id_ed25519", ruleSSH)
	w.refused(a, Read, "~/share/sub/key", ruleSSH)
	w.refused(a, Read, "~/share/../.ssh/id_ed25519", ruleSSH)
	// A link as the parent of a new file.
	w.refused(a, Write, "~/share/keys/authorized_keys", ruleSSH)
	w.refused(a, Write, "~/share/out/new.txt", `it is not in "allow" of the tool read`)
	w.refused(a, Write, "~/share/out", `it is not in "allow" of the tool read`)
	w.refused(a, Read, "~/share/../notes.md", `it is not in "allow" of the tool read`)
	// ".." after a link is the directory above where the link leads.
	w.refused(a, Read, "~/share/deep/../x", `it is not in "allow" of the tool read`)
	w.allowed(Access{Tool: "read"}, Read, "~/share/deep/../x", w.root+"/elsewhere/x")
	w.allowed(Access{Tool: "read"}, Read, "/../..", "/")
	// Within what is allowed a link is followed.
	w.allowed(a, Read, "~/share/again", w.home+"/share/hello.txt")
	w.allowed(a, Read, "~/share/./sub/../hello.txt", w.home+"/share/hello.txt")
	// Deleting takes the link itself, so it is the link that has to be allowed.
	w.allowed(a, Delete, "~/share/keys", w.home+"/share/keys")
	w.refused(a, Delete, "~/share/keys/id_ed25519", ruleSSH)
	w.refused(a, Delete, "~/share/keys/", ruleSSH)

	// An allowed place that is a link allows where it leads.
	b := Access{Tool: "read", Allow: paths("~/share/deep"), Peer: true}
	w.allowed(b, Read, w.root+"/elsewhere/deep/x", w.root+"/elsewhere/deep/x")
	w.allowed(b, Read, "~/share/deep/x", w.root+"/elsewhere/deep/x")

	// Links in a circle end.
	w.link("b", w.home+"/share/a")
	w.link("a", w.home+"/share/b")
	if got, err := w.check(a, Read, "~/share/a/x"); err == nil || got != "" || !strings.HasSuffix(err.Error(), "too many symbolic links") {
		t.Errorf("a circle: %q, %v", got, err)
	}
	// As many links as the kernel follows are followed.
	prev := "hello.txt"
	for i := range maxLinks {
		name := "l" + strconv.Itoa(i) // not letters: some file systems ignore their case
		w.link(prev, w.home+"/share/"+name)
		prev = name
	}
	w.allowed(a, Read, "~/share/"+prev, w.home+"/share/hello.txt")
	w.link(prev, w.home+"/share/last")
	if _, err := w.check(a, Read, "~/share/last"); err == nil {
		t.Error("one link more than the limit was followed")
	}

	// A link that cannot be read leads nowhere.
	readlink = func(string) (string, error) { return "", errTest }
	defer func() { readlink = os.Readlink }()
	if got, err := w.check(Access{Tool: "read"}, Read, "~/share/again"); !errors.Is(err, errTest) || got != "" {
		t.Errorf("a link that cannot be read: %q, %v", got, err)
	}
}

// The source of ouroboros is changed like any other file: the core and what
// decides how it is built too.
func TestSourceOfOuro(t *testing.T) {
	w := newWorld(t)
	src := w.home + "/src/ouro"
	w.file(src+"/go.mod", "module ouroboros\n")
	w.file(src+"/internal/guard/guard.go", "package guard")
	a := Access{Tool: "edit"}
	for _, path := range []string{"internal/guard/guard.go", "internal/a2a/new.go", "go.mod", "Makefile", "internal/cli/cli.go"} {
		for _, op := range []Op{Write, Delete} {
			w.allowed(a, op, src+"/"+path, src+"/"+path)
		}
	}
}

// The Go that builds ouro is not changed, nor is a go put where rebuild
// would look for it: rebuild runs it with network.
func TestToolchain(t *testing.T) {
	w := newWorld(t)
	w.file(w.home+"/go/bin/go", "the go")
	w.link(w.home+"/go/bin/go", w.home+"/bin/go")
	w.guard.Toolchain = func() []string {
		return []string{w.home + "/bin/go", w.home + "/path/go", w.home + "/go"}
	}
	a := Access{Tool: "write"}
	for _, op := range []Op{Write, Delete} {
		w.refused(a, op, "~/go/bin/go", ruleToolchain)
		w.refused(a, op, "~/go/src/new.go", ruleToolchain)
		w.refused(a, op, "~/bin/go", ruleToolchain) // the link, and where it leads
		w.refused(a, op, "~/path/go", ruleToolchain)
		w.allowed(a, op, "~/path/gopher", w.home+"/path/gopher")
	}
	w.allowed(a, Read, "~/bin/go", w.home+"/go/bin/go")

	// A path of Go that leads nowhere, as one below a file in PATH, is
	// passed over: nothing can be written there.
	w.link(w.home+"/loop", w.home+"/loop")
	w.guard.Toolchain = func() []string { return []string{w.home + "/loop/go", w.home + "/notes.md/go", w.home + "/go"} }
	w.file(w.home+"/notes.md", "a file")
	w.refused(a, Write, "~/go/bin/go", ruleToolchain)
	w.allowed(a, Write, "~/other", w.home+"/other")
}

func TestRefusalNamesTheRule(t *testing.T) {
	err := &Refusal{Op: Write, Path: "~/x", Rule: "no"}
	if err.Error() != "writing ~/x is refused: no" {
		t.Errorf("got %q", err.Error())
	}
	if Read.String() != "reading" || Delete.String() != "deleting" {
		t.Errorf("got %v, %v", Read, Delete)
	}
}

// The sandbox of a command holds the rules for files: the secrets, what is
// protected and what the tool is denied are neither read nor written, the
// node and the Go of rebuild are not written, and the rest is writable as
// "allow" says.
func TestBox(t *testing.T) {
	w := newWorld(t)
	w.guard.Toolchain = func() []string { return []string{w.home + "/go", w.home + "/bin/go"} }
	p, err := w.guard.Box(Access{Tool: "bash", Deny: []string{"~/share"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Writable, []string{"/"}) || !p.Network {
		t.Errorf("for the owner without allow: %+v", p)
	}
	hidden := []string{w.node + "/config.json", w.node + "/config.json.lock", w.home + "/.ssh", w.home + "/share"}
	if !slices.Equal(p.Hidden, hidden) {
		t.Errorf("hidden: %q", p.Hidden)
	}
	readOnly := []string{w.node, w.home + "/go", w.home + "/bin/go"}
	if !slices.Equal(p.ReadOnly, readOnly) {
		t.Errorf("read only: %q", p.ReadOnly)
	}

	// What "allow" names is writable as far as it exists; a peer without
	// it writes nothing.
	// The owner's commands write temporary files too.
	t.Setenv("TMPDIR", w.home+"/tmp")
	p, err = w.guard.Box(Access{Tool: "bash", Allow: paths("~/share", "~/missing", w.root)}, false)
	if err != nil || !slices.Equal(p.Writable, []string{w.home + "/share", w.root}) || p.Network {
		t.Errorf("with allow, without the temporary files: %+v, %v", p, err)
	}
	w.file(w.home+"/tmp/x", "")
	w.file(w.home+"/work/x", "")
	w.guard.Workspace = w.home + "/work"
	p, err = w.guard.Box(Access{Tool: "bash", Allow: paths("~/share")}, false)
	if err != nil || !slices.Equal(p.Writable, []string{w.home + "/share", w.home + "/tmp", w.home + "/work"}) {
		t.Errorf("with allow: %+v, %v", p, err)
	}
	if p, err := w.guard.Box(Access{Tool: "bash", Allow: paths("~/share"), Peer: true}, false); err != nil || !slices.Equal(p.Writable, []string{w.home + "/share"}) {
		t.Errorf("a peer with allow: %+v, %v", p, err)
	}
	if p, err := w.guard.Box(Access{Tool: "bash", Peer: true}, false); err != nil || len(p.Writable) != 0 {
		t.Errorf("for a peer: %+v, %v", p, err)
	}
	w.guard.Toolchain = nil
	if p, err := w.guard.Box(Access{Tool: "bash"}, false); err != nil || !slices.Equal(p.ReadOnly, []string{w.node}) {
		t.Errorf("without toolchain: %+v, %v", p, err)
	}

	// Rules that cannot be read run nothing.
	for _, a := range []Access{{Allow: paths("relative")}, {Deny: []string{"relative"}}} {
		if _, err := w.guard.Box(a, false); err == nil {
			t.Errorf("%+v: no error", a)
		}
	}
	w.protected = []string{"relative"}
	if _, err := w.guard.Box(Access{}, false); err == nil || !strings.Contains(err.Error(), `"protected"`) {
		t.Errorf("protected that is no path: %v", err)
	}
	w.protected, w.failing = nil, errTest
	if _, err := w.guard.Box(Access{}, false); !errors.Is(err, errTest) {
		t.Errorf("a configuration that cannot be read: %v", err)
	}
}

// A file system that does not tell names apart by their case, like that of
// macOS, opens a file by any case of its name: the rules hold for every one.
func TestCaseOfNames(t *testing.T) {
	w := newWorld(t)
	w.file(w.home+"/prot/inner/x", "x")
	if _, err := os.Lstat(w.home + "/PROT"); err != nil {
		t.Skip("the file system tells names apart by their case")
	}
	w.protected = []string{"~/.ssh", "~/prot"}
	rule := `~/prot is in "protected" of the configuration, which no tool touches`
	owner := Access{Tool: "read"}
	for _, op := range []Op{Read, Write, Delete} {
		w.refused(owner, op, "~/PROT/inner/x", rule)
		w.refused(owner, op, "~/Prot/INNER", rule)
		w.refused(owner, op, "~/.SSH/id_ed25519", ruleSSH)
	}
	w.refused(owner, Write, w.node+"/../"+filepath.Base(w.node)+"/CONTACTS.json", ruleState)
	w.refused(owner, Delete, strings.ToUpper(w.home), ruleRoot)
	// A protected path that is not there yet is protected however it is
	// written, and so is one whose directory cannot be read.
	w.protected = []string{"~/.netrc", "~/locked/x"}
	w.refused(owner, Write, "~/.NETRC", `~/.netrc is in "protected" of the configuration, which no tool touches`)
	w.file(w.home+"/locked/x", "")
	os.Chmod(w.home+"/locked", 0o300)
	w.refused(owner, Read, "~/locked/X", `~/locked/x is in "protected" of the configuration, which no tool touches`)
	os.Chmod(w.home+"/locked", 0o755)
	os.RemoveAll(w.home + "/locked")
	w.protected = []string{"~/.ssh", "~/prot"}
	// What is not there yet is taken as it is written.
	w.allowed(owner, Write, "~/Share/new.txt", w.home+"/share/new.txt")
	w.allowed(owner, Write, "~/share/NEW/x", w.home+"/share/NEW/x")

	// A name the directory has in another form, as macOS has "ä" written
	// as "a" and the dots, is the same.
	w.file(w.home+"/share/\u00e4", "")
	if _, err := os.Lstat(w.home + "/share/a\u0308"); err == nil {
		w.allowed(owner, Read, "~/share/a\u0308", w.home+"/share/\u00e4")
	}

	// A directory that cannot be read, or has the name no more, leaves it
	// as it is written.
	w.file(w.home+"/locked/x", "")
	if err := os.Chmod(w.home+"/locked", 0o300); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(w.home+"/locked", 0o755)
	w.allowed(owner, Read, "~/locked/X", w.home+"/locked/X")
	// Of many directories the first are forgotten.
	for i := range maxListings {
		names.dirs[strconv.Itoa(i)] = listing{}
	}
	w.file(w.home+"/fresh/x", "")
	w.allowed(owner, Read, "~/FRESH/x", w.home+"/fresh/x")
	if len(names.dirs) >= maxListings {
		t.Errorf("%d directories are kept", len(names.dirs))
	}
	defer func(read func(string) ([]string, error)) { readNames = read }(readNames)
	readNames = func(string) ([]string, error) { return nil, errTest }
	clear(names.dirs)
	w.allowed(owner, Read, "~/share/HELLO.txt", w.home+"/share/HELLO.txt")
	readNames = func(string) ([]string, error) { return []string{"gone"}, nil }
	clear(names.dirs)
	w.allowed(owner, Read, "~/share/HELLO.txt", w.home+"/share/HELLO.txt")
}

// A hard link is the file it links: one to a file that is protected or
// denied is refused as that file is.
func TestHardLinks(t *testing.T) {
	w := newWorld(t)
	w.file(w.home+"/.netrc", "machine x")
	w.file(w.home+"/tax/2026.txt", "1234")
	w.protected = []string{"~/.netrc", "~/tax"}
	for _, link := range [][2]string{{"/.netrc", "/share/netrc"}, {"/tax/2026.txt", "/share/2026.txt"}} {
		if err := os.Link(w.home+link[0], w.home+link[1]); err != nil {
			t.Fatal(err)
		}
	}
	owner := Access{Tool: "read"}
	w.refused(owner, Read, "~/share/netrc", `~/.netrc is in "protected" of the configuration, which no tool touches`)
	// Of a directory, what it holds is not told by its file: what is linked
	// from it is not known.
	w.allowed(owner, Read, "~/share/2026.txt", w.home+"/share/2026.txt")
	w.protected = nil
	denied := Access{Tool: "read", Deny: []string{"~/.netrc"}}
	w.refused(denied, Read, "~/share/netrc", `~/.netrc is in "deny" of the tool read`)
	w.allowed(denied, Read, "~/share/hello.txt", w.home+"/share/hello.txt")
}
