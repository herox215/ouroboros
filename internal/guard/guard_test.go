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
	ruleCore   = "it is source of the core of ouroboros, which no tool changes"
	ruleBuild  = "it decides how ouroboros is built and what the build fetches and runs, which no tool changes"
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

// A path the tool is given has to be absolute or below "~".
func TestPathsOfTheTool(t *testing.T) {
	w := newWorld(t)
	a := Access{Tool: "read"}
	for _, path := range []string{"", "notes.md", "./notes.md", "~user/notes.md", "../x"} {
		if got, err := w.check(a, Read, path); err == nil || got != "" || err.Error() != `the path "`+path+`" has to be absolute or start with ~` {
			t.Errorf("%q: %q, %v", path, got, err)
		}
	}
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

// The state of the node is read and never changed.
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

// The core of ouroboros is not changed in any checkout of it, nor what
// decides how it is built; the plugins next to it are.
func TestSourceOfTheCore(t *testing.T) {
	w := newWorld(t)
	src := w.home + "/src/ouro"
	w.file(src+"/go.mod", "// the module\nmodule ouroboros // this one\n\ngo 1.27.0\n")
	w.file(src+"/internal/a2a/a2a.go", "package a2a")
	w.file(src+"/internal/plugins/files/files.go", "package files")
	w.file(src+"/internal/go.mod", "module elsewhere\n")
	w.file(w.home+"/src/other/go.mod", "module other\n")
	w.file(w.home+"/src/other/internal/a2a/a2a.go", "package a2a")
	w.link(src+"/internal/identity", w.home+"/share/id")
	a := Access{Tool: "edit"}

	for _, pkg := range corePackages {
		for _, op := range []Op{Write, Delete} {
			w.refused(a, op, src+"/"+pkg, ruleCore)
			w.refused(a, op, src+"/"+pkg+"/new.go", ruleCore)
			w.refused(a, op, src+"/"+pkg+"/sub/new.go", ruleCore)
			w.allowed(a, op, src+"/"+pkg+"x/new.go", src+"/"+pkg+"x/new.go")
		}
		w.allowed(a, Read, src+"/"+pkg+"/new.go", src+"/"+pkg+"/new.go")
	}
	if len(corePackages) != 10 || corePackages[9] != "internal/guard" || !slices.Contains(corePackages, "internal/sandbox") {
		t.Errorf("the packages of the core: %v", corePackages)
	}
	w.refused(a, Write, "~/share/id/identity.go", ruleCore)
	for _, pkg := range []string{"internal/daemon", "internal/kernel"} {
		w.allowed(a, Write, src+"/"+pkg+"/new.go", src+"/"+pkg+"/new.go")
	}

	// What decides what a build fetches and runs, and where the frame
	// hands out what plugins do not get.
	for _, path := range buildPaths {
		for _, op := range []Op{Write, Delete} {
			w.refused(a, op, src+"/"+path, ruleBuild)
			w.allowed(a, op, w.home+"/src/other/"+path, w.home+"/src/other/"+path)
		}
		w.allowed(a, Read, src+"/"+path, src+"/"+path)
	}
	for _, path := range []string{"internal/cli/cli.go", "cmd/ouro/main.go", "scripts/new.sh"} {
		w.refused(a, Write, src+"/"+path, ruleBuild)
	}
	if len(buildPaths) != 9 {
		t.Errorf("what decides the build: %v", buildPaths)
	}
	for _, op := range []Op{Write, Delete} {
		w.allowed(a, op, src+"/internal/plugins/files/files.go", src+"/internal/plugins/files/files.go")
		w.allowed(a, op, src+"/internal", src+"/internal")
		w.allowed(a, op, src+"/Makefile.local", src+"/Makefile.local")
		w.allowed(a, op, w.home+"/src/other/internal/a2a/a2a.go", w.home+"/src/other/internal/a2a/a2a.go")
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

func TestModuleOf(t *testing.T) {
	for content, want := range map[string]string{
		"module ouroboros\n":                       "ouroboros",
		"go 1.27\n\nmodule ouroboros":              "ouroboros",
		"module \"ouroboros\"\n":                   "ouroboros",
		"module `ouroboros`\n":                     "ouroboros",
		"module (\n\touroboros\n)\n":               "ouroboros",
		"module example.com/x // module ouroboros": "example.com/x",
		"// module ouroboros\nmodule x\n":          "x",
		"require module v1\n":                      "v1",
		"":                                         "",
		"go 1.27\n":                                "",
		"module":                                   "",
		"module (":                                 "",
	} {
		if got := moduleOf([]byte(content)); got != want {
			t.Errorf("moduleOf(%q) = %q, want %q", content, got, want)
		}
	}
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
