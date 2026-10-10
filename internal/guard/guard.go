// Package guard decides which files the tools of an agent may touch. Every
// tool for files asks it for every path, and works on the path it answers
// with: the one behind every symbolic link and every "..", so that none of
// them leads around a rule. A tool that runs commands, whose paths it never
// sees, runs them in the sandbox Box returns, with the same rules.
//
// Three things limit a tool, and the first that speaks wins:
//
//  1. The core, fixed here: the secrets of the node are neither read nor
//     changed, and neither the state of the node, nor the source of the core
//     of ouroboros, nor what decides how it is built, nor the Go it is built
//     with is changed. That an agent cannot rewrite the frame it runs in is
//     a rule of the frame, not of a prompt and not of the configuration.
//  2. "protected" in the configuration: paths no tool touches for anybody.
//  3. "allow" and "deny" of the tool for the kind of conversation it runs in.
package guard

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"ouroboros/internal/sandbox"
)

// Module is the name of the Go module of ouroboros. A directory whose go.mod
// names it is a checkout of the source.
const Module = "ouroboros"

// goMod is the file that names the module of a checkout.
const goMod = "go.mod"

// corePackages are the packages of the core, below the directory of a
// checkout: those that hold the keys and the secrets, decide whom the node
// trusts, and keep the rules of the tools and of rebuild. No tool changes
// them; the plugins and the other packages next to them stay writable.
var corePackages = []string{
	"internal/identity", "internal/a2a", "internal/contacts", "internal/config", "internal/home",
	"internal/update", "internal/sandbox", "internal/safefile", "internal/peers",
	"internal/guard",
}

// buildPaths are what decides what a build of a checkout fetches and runs,
// below the directory of the checkout, and where the plugins are handed
// what only the frame may hand out: no tool changes them either.
var buildPaths = []string{
	"go.mod", "go.sum", "go.work", "go.work.sum", "Makefile", "install.sh", "scripts", "internal/cli", "cmd",
}

// maxLinks is how many symbolic links one path may lead through, like the
// limit of the kernel: a circle of links ends there.
const maxLinks = 40

// readlink reads a symbolic link; tests replace it to get in the way.
var readlink = os.Readlink

// Op is what a tool does with a path.
type Op int

// The things a tool does with a path.
const (
	Read   Op = iota // read a file, or list a directory
	Write            // create a file or replace its content
	Delete           // remove a file or a directory
)

func (o Op) String() string {
	return [...]string{"reading", "writing", "deleting"}[o]
}

// Access is what the owner allows one tool in one kind of conversation.
type Access struct {
	Tool string // the name of the tool, for the refusal
	// Allow are the paths the tool may go to: files, or directories with
	// everything below them. Nil is everything for the owner and nothing for
	// a peer.
	Allow *[]string
	// Deny are paths within Allow the tool does not go to.
	Deny []string
	// Peer says that the conversation comes from a peer, not from the owner.
	Peer bool
}

// Refusal is the error of a path a tool may not touch. It names the rule, so
// that the agent can tell why.
type Refusal struct {
	Op   Op
	Path string // as the tool was given it
	Rule string
}

func (r *Refusal) Error() string {
	return fmt.Sprintf("%s %s is refused: %s", r.Op, r.Path, r.Rule)
}

// Guard holds what is fixed for the node.
type Guard struct {
	// Workspace is where commands start that name no directory; "" for
	// the home directory of the user.
	Workspace string
	// Home is the directory of the node. Nothing in it is changed.
	Home string
	// Secrets are the files that are neither read nor changed, each with
	// everything below it.
	Secrets []string
	// Protected returns "protected" of the configuration as it is now.
	Protected func() ([]string, error)
	// Toolchain returns the paths of the Go that builds ouro, which are
	// read and never changed, each with everything below it. May be nil.
	Toolchain func() []string
	// Checkouts returns the checkouts of ouroboros a sandbox keeps as the
	// rules for checkouts say, see Box. May be nil.
	Checkouts func() []string
}

// Checker decides about paths for one call of a tool.
type Checker struct {
	access    Access
	home      string
	userHome  string // "" if it is not known
	secrets   []secret
	toolchain []string
	protected []rule
	allow     []rule
	deny      []rule
}

// rule is a path of the configuration: as it is written there, and where it
// leads.
type rule struct{ written, real string }

// secret is a file that is not read: where it is, and the file itself, by
// which a hard link to it is recognised.
type secret struct {
	real string
	info fs.FileInfo // nil if the file does not exist
}

// Checker returns what decides about paths for a call of the tool of a. It
// fails if a rule cannot be read: no file is touched then.
func (g *Guard) Checker(a Access) (*Checker, error) {
	c := &Checker{access: a}
	var err error
	if c.home, err = resolve(g.Home, true); err != nil {
		return nil, err
	}
	if home, err := os.UserHomeDir(); err == nil {
		if c.userHome, err = resolve(home, true); err != nil {
			return nil, err
		}
	}
	for _, path := range g.Secrets {
		real, err := resolve(path, true)
		if err != nil {
			return nil, err
		}
		info, _ := os.Stat(real)
		c.secrets = append(c.secrets, secret{real, info})
	}
	if g.Toolchain != nil {
		for _, path := range g.Toolchain() {
			// A link counts as itself, and as where it leads. A path that
			// leads nowhere, like one below a file, cannot be written
			// either: it is asked again with the next call.
			for _, follow := range []bool{false, true} {
				if real, err := resolve(path, follow); err == nil {
					c.toolchain = append(c.toolchain, real)
				}
			}
		}
	}
	protected, err := g.Protected()
	if err != nil {
		return nil, fmt.Errorf("the configuration cannot be read, so no file is touched: %w", err)
	}
	if c.protected, err = rules(protected); err != nil {
		return nil, fmt.Errorf(`"protected" in the configuration: %w`, err)
	}
	if a.Allow != nil {
		if c.allow, err = rules(*a.Allow); err != nil {
			return nil, err
		}
	}
	c.deny, err = rules(a.Deny)
	return c, err
}

// Box returns the sandbox for a command that the tool of a runs, which the
// guard is not asked about: it holds the rules of the guard for what is
// written and read, as far as a sandbox can.
//
// It knows no rule a guard decides when a path is asked: a checkout is one
// of Checkouts, not every directory whose go.mod names the module; / and the
// home directory are not kept from being deleted, only what the rules keep
// in them; a path that "allow" names and that does not exist is not
// writable.
func (g *Guard) Box(a Access, network bool) (sandbox.Policy, error) {
	p := sandbox.Policy{Network: network}
	protected, err := g.Protected()
	if err != nil {
		return p, fmt.Errorf("the configuration cannot be read, so no command runs: %w", err)
	}
	closed, err := expandAll(protected)
	if err != nil {
		return p, fmt.Errorf(`"protected" in the configuration: %w`, err)
	}
	deny, err := expandAll(a.Deny)
	if err != nil {
		return p, err
	}
	// What no tool touches is neither read nor written.
	p.Hidden = slices.Concat(g.Secrets, closed, deny)
	switch {
	case a.Allow != nil:
		allow, err := expandAll(*a.Allow)
		if err != nil {
			return p, err
		}
		for _, path := range allow {
			if _, err := os.Stat(path); err == nil {
				p.Writable = append(p.Writable, path)
			}
		}
	case !a.Peer:
		p.Writable = []string{"/"}
	}
	p.ReadOnly = []string{g.Home}
	if g.Toolchain != nil {
		p.ReadOnly = append(p.ReadOnly, g.Toolchain()...)
	}
	if g.Checkouts != nil {
		for _, dir := range g.Checkouts() {
			for _, rel := range slices.Concat(corePackages, buildPaths) {
				p.ReadOnly = append(p.ReadOnly, filepath.Join(dir, rel))
			}
		}
	}
	return p, nil
}

// expandAll expands every path of a list of the configuration.
func expandAll(paths []string) ([]string, error) {
	var out []string
	for _, path := range paths {
		abs, err := expand(path)
		if err != nil {
			return nil, err
		}
		out = append(out, abs)
	}
	return out, nil
}

// rules resolves the paths of a list of the configuration.
func rules(paths []string) ([]rule, error) {
	var list []rule
	for _, written := range paths {
		abs, err := expand(written)
		if err != nil {
			return nil, err
		}
		real, err := resolve(abs, true)
		if err != nil {
			return nil, err
		}
		list = append(list, rule{written, real})
	}
	return list, nil
}

// Valid reports whether every path of a list of the configuration is one:
// absolute, or below "~".
func Valid(paths []string) error {
	for _, path := range paths {
		if _, err := expand(path); err != nil {
			return err
		}
	}
	return nil
}

// Check decides whether the tool may do op with path. It returns the path to
// work on, with every symbolic link resolved, or a *Refusal. What is deleted
// is the path itself, so a link at its end is not followed for Delete.
func (c *Checker) Check(op Op, path string) (string, error) {
	abs, err := expand(path)
	if err != nil {
		return "", err
	}
	real, err := resolve(abs, op != Delete)
	if err != nil {
		return "", err
	}
	if rule := c.refuses(op, real); rule != "" {
		return "", &Refusal{Op: op, Path: path, Rule: rule}
	}
	return real, nil
}

// refuses returns the rule that keeps the tool from doing op with the
// resolved path real, or "" if none does.
func (c *Checker) refuses(op Op, real string) string {
	// 1. The core.
	if c.secret(real) {
		return "it holds the secrets of this node, which no tool reads or changes"
	}
	if op != Read && within(real, c.home) {
		return "it is in the directory of this node, where the node keeps its state: no tool changes it"
	}
	if op != Read {
		if rule := inCheckout(real); rule != "" {
			return rule
		}
		for _, t := range c.toolchain {
			if within(real, t) {
				return "it is part of the Go that rebuild builds ouroboros with, which no tool changes"
			}
		}
	}
	if op == Delete && (real == "/" || real == c.userHome) {
		return "the root of the file system and the home directory are never deleted"
	}
	// 2. What the owner protects from every tool.
	for _, p := range c.protected {
		if within(real, p.real) {
			return fmt.Sprintf(`%s is in "protected" of the configuration, which no tool touches`, p.written)
		}
	}
	// 3. What the owner allows this tool in this kind of conversation.
	for _, d := range c.deny {
		if within(real, d.real) {
			return fmt.Sprintf(`%s is in "deny" of the tool %s`, d.written, c.access.Tool)
		}
	}
	if c.access.Allow == nil {
		if c.access.Peer {
			return fmt.Sprintf(`the tool %s has no "allow" for peers, so it reaches nothing`, c.access.Tool)
		}
		return ""
	}
	for _, a := range c.allow {
		if within(real, a.real) {
			return ""
		}
	}
	return fmt.Sprintf(`it is not in "allow" of the tool %s`, c.access.Tool)
}

// secret reports whether real is one of the files that are not read, or
// another name of one, or below one that is a directory.
func (c *Checker) secret(real string) bool {
	for _, s := range c.secrets {
		if within(real, s.real) {
			return true
		}
		if s.info == nil {
			continue
		}
		// real has no link left to follow, except the one that is deleted.
		if info, err := os.Lstat(real); err == nil && os.SameFile(info, s.info) {
			return true
		}
	}
	return false
}

// inCheckout returns the rule that keeps real as it is in a checkout of
// ouroboros: in a package of the core, or among what decides how it is
// built. "" if none does. Every directory above it is asked, not only the
// nearest with a go.mod: a go.mod written in between does not take real out
// of its checkout.
func inCheckout(real string) string {
	for dir := filepath.Dir(real); ; dir = filepath.Dir(dir) {
		if moduleAt(filepath.Join(dir, goMod)) == Module {
			rel, _ := filepath.Rel(dir, real) // real is below dir: cannot fail
			for _, pkg := range corePackages {
				if within(rel, pkg) {
					return "it is source of the core of ouroboros, which no tool changes"
				}
			}
			for _, b := range buildPaths {
				if within(rel, b) {
					return "it decides how ouroboros is built and what the build fetches and runs, which no tool changes"
				}
			}
		}
		if dir == "/" {
			return ""
		}
	}
}

// moduleAt is the module the go.mod at path names, "" if there is none.
func moduleAt(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return moduleOf(b)
}

// moduleOf is the module a go.mod with this content names.
func moduleOf(content []byte) string {
	var words []string
	for line := range strings.Lines(string(content)) {
		line, _, _ = strings.Cut(line, "//")
		words = append(words, strings.Fields(line)...)
	}
	for i, word := range words {
		if word != "module" {
			continue
		}
		rest := words[i+1:]
		if len(rest) > 0 && rest[0] == "(" {
			rest = rest[1:]
		}
		if len(rest) > 0 {
			return strings.Trim(rest[0], "\"`")
		}
	}
	return ""
}

// within reports whether path is root or below it.
func within(path, root string) bool {
	return path == root || strings.HasPrefix(path, strings.TrimSuffix(root, "/")+"/")
}

// expand returns path with "~" replaced by the home directory of the user.
// A path has to be absolute or start there: a node has no place of its own
// that a relative one could start from.
func expand(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("the path %s needs the home directory, which is not known: %w", path, err)
		}
		path = home + path[1:]
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("the path %q has to be absolute or start with ~", path)
	}
	return path, nil
}

// resolve returns where the absolute path leads: without symbolic links, "."
// and "..". What does not exist yet is taken as it is written, for it is no
// link. With followLast unset a link at the very end is kept.
func resolve(path string, followLast bool) (string, error) {
	rest := strings.Split(path, "/")
	real, links := "/", 0
	for len(rest) > 0 {
		name := rest[0]
		rest = rest[1:]
		if name == ".." {
			real = filepath.Dir(real)
			continue
		}
		// An empty name and "." are the directory itself, like any name that
		// is no link.
		next := filepath.Join(real, name)
		info, err := os.Lstat(next)
		if errors.Is(err, fs.ErrNotExist) {
			real = next
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&fs.ModeSymlink == 0 || (len(rest) == 0 && !followLast) {
			real = next
			continue
		}
		if links++; links > maxLinks {
			return "", fmt.Errorf("%s: too many symbolic links", path)
		}
		target, err := readlink(next)
		if err != nil {
			return "", err
		}
		if filepath.IsAbs(target) {
			real = "/"
		}
		rest = append(strings.Split(target, "/"), rest...)
	}
	return real, nil
}
