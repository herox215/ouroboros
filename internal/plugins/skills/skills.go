// Package skills is the plugin that gives the agents the skills of the
// owner: instructions for a kind of task, which the agent loads when a task
// of that kind comes. A skill is a directory in the skills directory of the
// node with a file SKILL.md, in the format of Agent Skills:
//
//	---
//	name: review
//	description: How to review a change. Use when the owner asks for a review.
//	---
//	The instructions …
//
// The name and the description of every skill the agent may use are in the
// description of the tool skill; the instructions come only when the agent
// loads them. Further files of a skill are read through the tool as well, so
// that an agent without the tools for files can use them too.
//
// The tools for files cannot change the node directory. The owner can
// create and update skills through the dedicated skill_write tool.
package skills

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"ouroboros/internal/assistant"
	"ouroboros/internal/home"
	"ouroboros/plugin"
)

// Tool is the name of the tool.
const Tool = assistant.ToolSkill

// file is the file of a skill that holds its name, its description and its
// instructions.
const file = "SKILL.md"

// maxFile is the most the tool reads of a file of a skill.
const maxFile = 256 << 10

// validName is what the name of a skill looks like, as Agent Skills has it.
var validName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Plugin contributes the tool that loads the skills in the skills
// directory of dir, and what the owner is shown of its calls. Every call is
// logged in dir.
func Plugin(dir home.Dir) plugin.Plugin {
	return plugin.New("skills", func(ctx *plugin.Context, _ struct{}) error {
		l := library{dir.Skills(), dir.Log}
		plugin.Add(ctx, l.tool())
		plugin.Add(ctx, l.writeTool())
		plugin.Add(ctx, plugin.ToolView{Tool: Write, Show: showWrite, Detail: assistant.HowItEnded})
		plugin.Add(ctx, plugin.ToolView{Tool: Tool, Show: show, Detail: assistant.HowItEnded})
		return nil
	})
}

// show is what the owner sees of a call.
func show(raw json.RawMessage) string {
	var args loadArgs
	if json.Unmarshal(raw, &args) != nil || args.Name == "" {
		return "loading a skill"
	}
	if args.File != "" {
		return "reading " + args.File + " of the skill " + args.Name
	}
	return "loading the skill " + args.Name
}

// library is the directory of the skills and the log of the node.
type library struct {
	dir string
	log func(format string, args ...any)
}

// skill is what the description of the tool says of a skill.
type skill struct {
	Name, Description string
}

// settings are the settings of the tool: the skills it may load. Without
// them it loads every skill for the owner and none for a peer.
type settings struct {
	Allow *[]string `json:"allow" about:"the skills it may load, by name" unset:"every skill for your agent, none for a peer"`
}

type loadArgs struct {
	Name string `json:"name"`
	File string `json:"file"`
}

func (l library) tool() plugin.Tool {
	return plugin.Tool{
		Name:        Tool,
		Description: describe(nil),
		Schema: `{"type":"object","properties":{` +
			`"name":{"type":"string","description":"the name of the skill"},` +
			`"file":{"type":"string","description":"a further file of the skill, by its path within the skill as SKILL.md names it; left out: SKILL.md"}},` +
			`"required":["name"]}`,
		Run:       l.run(nil),
		Configure: l.configure,
		Options:   plugin.OptionsOf[settings](),
	}
}

// configure returns the tool with the settings raw. The skills are read
// here, when the agent is built: a skill that is added is offered once the
// agent is built again.
func (l library) configure(raw json.RawMessage) (plugin.Tool, error) {
	var s settings
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return plugin.Tool{}, fmt.Errorf("settings of the tool %s: %w", Tool, err)
	}
	all, err := l.skills()
	if err != nil {
		return plugin.Tool{}, fmt.Errorf("settings of the tool %s: %w", Tool, err)
	}
	offered := all
	if s.Allow != nil {
		offered = nil
		for _, name := range *s.Allow {
			at := slices.IndexFunc(all, func(k skill) bool { return k.Name == name })
			if at < 0 {
				return plugin.Tool{}, fmt.Errorf("settings of the tool %s: unknown skill %q (there are: %s)", Tool, name, names(all))
			}
			offered = append(offered, all[at])
		}
	}
	t := l.tool()
	t.Description, t.Run = describe(offered), l.run(s.Allow)
	return t, nil
}

// describe is the description of the tool that offers skills.
func describe(skills []skill) string {
	d := "Load a skill: instructions of the owner for a kind of task. Before you start a task a skill is for, " +
		"load it and follow what it says for the rest of the conversation; what a skill says of the files of " +
		"the skill, read with file. "
	if len(skills) == 0 {
		return d + "There are no skills."
	}
	d += "The skills:"
	for _, k := range skills {
		d += "\n- " + k.Name + ": " + k.Description
	}
	return d
}

// names lists the names of skills for an error.
func names(skills []skill) string {
	if len(skills) == 0 {
		return "none"
	}
	out := make([]string, len(skills))
	for i, k := range skills {
		out[i] = k.Name
	}
	return strings.Join(out, ", ")
}

// run is what runs the tool that may load the skills allow, nil for every
// skill for the owner and none for a peer.
func (l library) run(allow *[]string) plugin.RunFunc {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var args loadArgs
		if err := plugin.Decode(Tool, raw, &args); err != nil {
			return "", err
		}
		_, peer := plugin.OriginFrom(ctx)
		party := "the owner"
		if peer {
			party = "a peer"
		}
		text, err := l.load(args, allow, peer)
		if err != nil {
			l.log("tool %s for %s: %s, failed: %v", Tool, party, args.Name, err)
			return "", err
		}
		l.log("tool %s for %s: %s %s", Tool, party, args.Name, args.File)
		return text, nil
	}
}

// load returns the instructions of the skill args names, or the file of it
// args names.
func (l library) load(args loadArgs, allow *[]string, peer bool) (string, error) {
	if args.Name == "" {
		return "", errors.New(`skill needs "name", the name of the skill to load`)
	}
	if !validName.MatchString(args.Name) {
		return "", fmt.Errorf("there is no skill %q", args.Name)
	}
	switch {
	case allow != nil && !slices.Contains(*allow, args.Name):
		return "", fmt.Errorf("the skill %s is not allowed here", args.Name)
	case allow == nil && peer:
		return "", errors.New("a peer gets only the skills the owner allows by name")
	}
	root := filepath.Join(l.dir, args.Name)
	if args.File == "" {
		k, body, err := read(root)
		if err != nil {
			return "", err
		}
		return "The skill " + k.Name + ":\n\n" + body, nil
	}
	path, err := within(root, args.File)
	if err != nil {
		return "", err
	}
	return text(path)
}

// within returns the file name of the skill in root, with every link
// resolved, or an error if it leads out of the skill.
func within(root, name string) (string, error) {
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("%s: a file of a skill is named by its path within the skill", name)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("there is no skill %q", filepath.Base(root))
	}
	real, err := filepath.EvalSymlinks(filepath.Join(realRoot, name))
	if err != nil {
		return "", fmt.Errorf("%s: no such file in the skill", name)
	}
	if rel, _ := filepath.Rel(realRoot, real); rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s: not a file of the skill", name)
	}
	return real, nil
}

// text returns the content of the file path, which has to be text of at most
// maxFile.
func text(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s: not a file", filepath.Base(path))
	}
	if info.Size() > maxFile {
		return "", fmt.Errorf("%s: more than %d KiB", filepath.Base(path), maxFile>>10)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(b) || slices.Contains(b, 0) {
		return "", fmt.Errorf("%s: not text", filepath.Base(path))
	}
	return string(b), nil
}

// skills are the skills of the library, by name. A directory without
// SKILL.md is no skill; a SKILL.md that cannot be used is an error.
func (l library) skills() ([]skill, error) {
	entries, err := os.ReadDir(l.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []skill
	for _, e := range entries {
		root := filepath.Join(l.dir, e.Name())
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, file)); errors.Is(err, os.ErrNotExist) {
			continue
		}
		k, _, err := read(root)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, nil
}

// read returns the skill in the directory root and its instructions.
func read(root string) (skill, string, error) {
	name := filepath.Base(root)
	content, err := text(filepath.Join(root, file))
	if errors.Is(err, os.ErrNotExist) {
		return skill{}, "", fmt.Errorf("there is no skill %q", name)
	}
	if err != nil {
		return skill{}, "", fmt.Errorf("skill %s: %w", name, err)
	}
	k, body, err := parse(content)
	if err != nil {
		return skill{}, "", fmt.Errorf("skill %s: %s: %w", name, filepath.Join(root, file), err)
	}
	if k.Name != name {
		return skill{}, "", fmt.Errorf("skill %s: %s names it %q: the name is that of its directory", name, filepath.Join(root, file), k.Name)
	}
	return k, body, nil
}

// parse splits a SKILL.md into its frontmatter and its instructions. Of the
// frontmatter only name and description are read, each on a line of its own.
func parse(content string) (skill, string, error) {
	content = strings.ReplaceAll(strings.TrimPrefix(content, "\ufeff"), "\r\n", "\n")
	rest, ok := strings.CutPrefix(content, "---\n")
	if !ok {
		return skill{}, "", errors.New("starts with no frontmatter between two lines ---")
	}
	head, body, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		if head, ok = strings.CutSuffix(rest, "\n---"); !ok {
			return skill{}, "", errors.New("the frontmatter has no line --- that ends it")
		}
	}
	var k skill
	for _, line := range strings.Split(head, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = unquote(strings.TrimSpace(value))
		switch strings.TrimSpace(key) {
		case "name":
			k.Name = value
		case "description":
			k.Description = value
		}
	}
	switch {
	case !validName.MatchString(k.Name):
		return skill{}, "", fmt.Errorf("the name %q is not of lowercase letters, digits and single hyphens", k.Name)
	case k.Description == "":
		return skill{}, "", errors.New("no description: it says when the skill is for")
	}
	return k, strings.TrimSpace(body), nil
}

// unquote takes the quotes off a value of YAML in quotes.
func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}
