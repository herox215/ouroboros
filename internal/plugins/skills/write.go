package skills

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"ouroboros/plugin"
)

// Write is the owner-only tool that creates and updates skill files.
const Write = "skill_write"

type writeArgs struct {
	Name      string `json:"name"`
	File      string `json:"file"`
	Content   string `json:"content"`
	Overwrite bool   `json:"overwrite"`
}

func showWrite(raw json.RawMessage) string {
	var a writeArgs
	if json.Unmarshal(raw, &a) != nil || a.Name == "" {
		return "writing a skill file"
	}
	if a.File == "" {
		a.File = file
	}
	return "writing " + a.File + " of the skill " + a.Name
}

func (l library) writeTool() plugin.Tool {
	return plugin.Tool{
		Name:        Write,
		OwnerOnly:   true,
		Description: "Create or update a skill file for the owner, only when the owner asks. The rest of the node directory stays protected. Start with SKILL.md: frontmatter between --- lines with name (matching the directory) and description, then instructions. Additional text files may follow. Existing files require overwrite: true; read them first. At most 256 KiB per file. New skills can be loaded immediately by name with skill; their descriptions are offered when the agent is built again. No deletion or execution.",
		Schema:      `{"type":"object","properties":{"name":{"type":"string","description":"Skill name: lowercase letters, digits and single hyphens"},"file":{"type":"string","description":"Relative file path within the skill; defaults to SKILL.md"},"content":{"type":"string","description":"Complete UTF-8 text of the file"},"overwrite":{"type":"boolean","description":"Explicitly replace an existing file; defaults to false"}},"required":["name","content"]}`,
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			if _, peer := plugin.OriginFrom(ctx); peer {
				return "", errors.New("only the owner's agent may write skills")
			}
			var a writeArgs
			if err := plugin.Decode(Write, raw, &a); err != nil {
				return "", err
			}
			if err := l.write(a); err != nil {
				return "", err
			}
			if l.log != nil {
				l.log("tool %s: %s %s", Write, a.Name, a.File)
			}
			return "Saved skill file. Load it with skill by name; new descriptions are offered when the agent is built again.", nil
		},
	}
}

// write uses a rooted filesystem and atomic replacement, never following a
// skill's symbolic links or modifying a partially written instruction file.
func (l library) write(a writeArgs) error {
	if !validName.MatchString(a.Name) {
		return errors.New("invalid skill name")
	}
	if a.File == "" {
		a.File = file
	}
	if !filepath.IsLocal(a.File) || filepath.Clean(a.File) != a.File || a.File == "." {
		return errors.New("file must be a clean relative path within the skill")
	}
	if len(a.Content) > maxFile || !utf8.ValidString(a.Content) || strings.ContainsRune(a.Content, 0) {
		return errors.New("content must be UTF-8 text of at most 256 KiB without NUL bytes")
	}
	if a.File == file {
		k, _, err := parse(a.Content)
		if err != nil {
			return err
		}
		if k.Name != a.Name {
			return errors.New("frontmatter name must match the skill directory")
		}
	}
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(l.dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("skills directory must not be a symbolic link")
	}
	root, err := os.OpenRoot(l.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Mkdir(a.Name, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err = root.Lstat(a.Name)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("skill directory must not be a symbolic link")
	}
	skill, err := root.OpenRoot(a.Name)
	if err != nil {
		return err
	}
	defer skill.Close()
	if a.File != file {
		// Auxiliary files cannot create an incomplete skill.
		b, err := skill.ReadFile(file)
		if err != nil {
			return fmt.Errorf("write a valid SKILL.md first: %w", err)
		}
		k, _, err := parse(string(b))
		if err != nil || k.Name != a.Name {
			return errors.New("write a valid SKILL.md first")
		}
	}
	parent := filepath.Dir(a.File)
	if parent != "." {
		current := ""
		for _, part := range strings.Split(parent, string(filepath.Separator)) {
			current = filepath.Join(current, part)
			if err := skill.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err := skill.Lstat(current)
			if err != nil {
				return err
			}
			if !info.IsDir() {
				return errors.New("skill file directories must not be symbolic links")
			}
		}
	}
	if info, err := skill.Lstat(a.File); err == nil {
		if !a.Overwrite {
			return errors.New("file already exists; read it first and set overwrite to true to replace it")
		}
		if !info.Mode().IsRegular() {
			return errors.New("target must be a regular file, not a symbolic link")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp := filepath.Join(parent, ".skill-"+rand.Text())
	f, err := skill.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer skill.Remove(tmp)
	_, err = f.WriteString(a.Content)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if !a.Overwrite {
		return skill.Link(tmp, a.File)
	}
	return skill.Rename(tmp, a.File)
}
