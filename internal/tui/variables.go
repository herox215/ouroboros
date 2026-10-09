package tui

import (
	"encoding/json"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/parser"
)

// The scripts of a conversation share one interpreter: what a script keeps
// at its top level is there for the scripts after it, and every result of
// run ends with a line that lists what is kept, like "[kept for the next
// script: cfg (string of 837 bytes)]". The context screen reads from these
// lines and from the scripts themselves what happened to every variable:
// which script set it, which scripts used it, what it is now, and when it was
// gone. The values are not in the context; only the interpreter of the node
// has them, as long as it runs.

// variable is a variable the interpreter keeps, as the context tells of it.
type variable struct {
	name string
	what string // what it was when it was last kept, like "string of 837 bytes"
	set  []int  // the numbers of the messages whose scripts set it
	used []int  // and of those whose scripts used it
	gone int    // and of the one after whose script it was no longer kept; 0 while it is
}

// keptVar is a name the interpreter keeps, with what it is.
type keptVar struct{ name, what string }

// runTrace is a call of run: its script, and what it uses of what the
// scripts before it kept.
type runTrace struct {
	script string
	uses   []string     // the names, in the order of their first use
	at     []nameAt     // where it uses them
	sets   []string     // the names it declares or assigns to
	number int          // the number of the message the call is in
	before []keptVar    // what was kept when it ran
	result *resultTrace // what was kept after it, nil until its result came
}

// nameAt is where a name is in a script: its offset in bytes.
type nameAt struct {
	name string
	at   int
}

// resultTrace is how what the interpreter keeps changed with a script: the
// result of run without its line of what is kept, and that line taken apart.
type resultTrace struct {
	text    string
	added   []keptVar
	changed []keptChange
	gone    []string
	same    []string
	more    string // like "and 3 more": the line lists only so many
}

// keptChange is a variable that a script set again: what it was before, and
// what it is now.
type keptChange struct {
	name, was, now string
}

// scriptTrace is what the scripts of a context did with their variables.
type scriptTrace struct {
	calls     map[int][]*runTrace  // by the index of a message: its calls in order, nil for a call of another tool
	results   map[int]*resultTrace // by the index of a message that is the result of a script
	variables []*variable          // in the order they were first kept
	byName    map[string]*variable
}

// traceScripts follows the variables of the scripts through msgs. A result
// belongs to the calls of the message before it, in their order. A result
// without a line of what is kept changed nothing, unless the script took
// the interpreter with it: then nothing is kept any more.
func traceScripts(msgs []Message) scriptTrace {
	t := scriptTrace{calls: map[int][]*runTrace{}, results: map[int]*resultTrace{}, byName: map[string]*variable{}}
	var kept []keptVar
	var waiting []*runTrace // the calls whose results did not come yet
	for i, c := range msgs {
		switch c.Role {
		case "assistant":
			waiting = nil
			for _, call := range splitCalls(c.Text) {
				script, ok := runScript(call)
				if !ok {
					t.calls[i], waiting = append(t.calls[i], nil), append(waiting, nil)
					continue
				}
				r := t.readRun(script, i+1, kept)
				t.calls[i], waiting = append(t.calls[i], r), append(waiting, r)
			}
		case "tool":
			var r *runTrace
			if len(waiting) > 0 {
				r, waiting = waiting[0], waiting[1:]
			}
			if now, ok := t.keptAfter(r, c.Text); ok {
				t.results[i], kept = r.result, now
			}
		}
	}
	return t
}

// readRun reads what the script of a call uses of what is kept, and counts
// those uses.
func (t *scriptTrace) readRun(script string, number int, kept []keptVar) *runTrace {
	r := &runTrace{script: script, number: number, before: kept}
	refs, declared, assigned := scriptNames(script)
	r.sets = append(declared, assigned...)
	for _, ref := range refs {
		// A name the script declares is a variable of its own.
		if slices.Contains(declared, ref.name) || !slices.ContainsFunc(kept, func(k keptVar) bool { return k.name == ref.name }) {
			continue
		}
		r.at = append(r.at, ref)
		if !slices.Contains(r.uses, ref.name) {
			r.uses = append(r.uses, ref.name)
			v := t.byName[ref.name]
			v.used = append(v.used, number)
		}
	}
	return r
}

// keptAfter reads from the result of the script of r what is kept after it,
// and how that changed; false when the result does not say, or is of no
// script.
func (t *scriptTrace) keptAfter(r *runTrace, result string) ([]keptVar, bool) {
	if r == nil {
		return nil, false
	}
	text, now, more, ok := parseKept(result)
	if !ok {
		if !strings.Contains(result, "what the scripts before it kept is gone") {
			return nil, false
		}
		text, now = result, nil
	}
	res := &resultTrace{text: text, more: more}
	for _, k := range now {
		i := slices.IndexFunc(r.before, func(b keptVar) bool { return b.name == k.name })
		if i < 0 {
			res.added = append(res.added, k)
		} else if r.before[i].what != k.what || slices.Contains(r.sets, k.name) {
			res.changed = append(res.changed, keptChange{k.name, r.before[i].what, k.what})
		} else {
			res.same = append(res.same, k.name)
			continue
		}
		v := t.byName[k.name]
		if v == nil {
			v = &variable{name: k.name}
			t.byName[k.name] = v
			t.variables = append(t.variables, v)
		}
		v.what, v.gone, v.set = k.what, 0, append(v.set, r.number)
	}
	// Only a line that lists everything tells what is gone.
	for _, b := range r.before {
		if more == "" && !slices.ContainsFunc(now, func(k keptVar) bool { return k.name == b.name }) {
			res.gone = append(res.gone, b.name)
			t.byName[b.name].gone = r.number
		}
	}
	r.result = res
	return now, true
}

var (
	// keptLine is the last line of a result of run, which lists what is kept.
	keptLine = regexp.MustCompile(`(?:^|\n)\[kept for the next script: (.*)\]$`)
	// keptItem is a name it lists, with what it is.
	keptItem = regexp.MustCompile(`^([\p{L}\p{N}_$]+) \((.+)\)$`)
)

// parseKept takes the line of what is kept off the result of a script: the
// text before it, the names it lists and what it says of the ones it does
// not list. False when the result has no such line.
func parseKept(result string) (text string, kept []keptVar, more string, ok bool) {
	loc := keptLine.FindStringSubmatchIndex(result)
	if loc == nil {
		return result, nil, "", false
	}
	for _, item := range strings.Split(result[loc[2]:loc[3]], ", ") {
		if m := keptItem.FindStringSubmatch(item); m != nil {
			kept = append(kept, keptVar{m[1], m[2]})
		} else {
			more = item
		}
	}
	return result[:loc[0]], kept, more, true
}

// splitCalls is the calls in what the agent says: what comes after every
// "→ " that begins a line.
func splitCalls(text string) []string {
	return strings.Split("\n"+text, "\n→ ")[1:]
}

// runScript is the script of a call of run; false for a call of another
// tool, or one without a script.
func runScript(call string) (string, bool) {
	name, args, _ := strings.Cut(call, " ")
	fields, ok := objectFields(args)
	if name != toolRun || !ok {
		return "", false
	}
	for _, f := range fields {
		var s string
		if f.key == "script" && json.Unmarshal(f.value, &s) == nil {
			return s, true
		}
	}
	return "", false
}

// toolRun is the name of the tool that runs a script.
const toolRun = "run"

// astPackage is the package of the nodes of a parsed script.
var astPackage = reflect.TypeFor[ast.Identifier]().PkgPath()

// scriptNames reads the names a script refers to, in the order they are in
// it, those it declares, also as a parameter or in a pattern, and those it
// assigns to. A name after a dot, a key and a label is no name it refers to,
// nor one it assigns to. A script that is no JavaScript has no names.
func scriptNames(script string) (refs []nameAt, declared, assigned []string) {
	program, err := parser.ParseFile(nil, "", script, 0)
	if err != nil {
		return nil, nil, nil
	}
	w := &names{script: script}
	w.walk(reflect.ValueOf(program), false)
	// The nodes are not all in the order of the script: a loop has its
	// update before its test.
	slices.SortFunc(w.refs, func(a, b nameAt) int { return a.at - b.at })
	return w.refs, w.declared, w.assigned
}

// names gathers the names of a script as it walks its nodes.
type names struct {
	script   string
	refs     []nameAt
	declared []string
	assigned []string
}

// walk walks the node v; under a declaration, a name is declared.
func (w *names) walk(v reflect.Value, declaring bool) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			w.walk(v.Elem(), declaring)
		}
	case reflect.Slice:
		for i := range v.Len() {
			w.walk(v.Index(i), declaring)
		}
	case reflect.Struct:
		if v.Type().PkgPath() == astPackage {
			w.node(v, declaring)
		}
	}
}

// node walks a node of the script, and takes the names from it.
func (w *names) node(v reflect.Value, declaring bool) {
	field := func(name string, declaring bool) { w.walk(v.FieldByName(name), declaring) }
	switch n := v.Interface().(type) {
	case ast.Identifier:
		w.name(n, declaring)
	case ast.PrivateIdentifier, ast.BranchStatement, ast.MetaProperty:
	case ast.DotExpression:
		field("Left", false) // what a property is of is used, also where the property is set
	case ast.PrivateDotExpression:
		field("Left", false)
	case ast.LabelledStatement:
		field("Statement", declaring)
	case ast.Binding:
		field("Target", true)
		field("Initializer", false)
	case ast.ForDeclaration:
		field("Target", true)
	case ast.AssignExpression:
		switch left := n.Left.(type) {
		case *ast.Identifier:
			w.assigned = appendNew(w.assigned, left.Name.String())
		case *ast.ObjectPattern, *ast.ArrayPattern:
			field("Left", true)
		default:
			field("Left", false) // a[i] = …: a is used
		}
		field("Right", false)
	case ast.PropertyKeyed:
		if n.Computed {
			field("Key", declaring)
		}
		field("Value", declaring)
	case ast.MethodDefinition:
		if n.Computed {
			field("Key", declaring)
		}
		field("Body", declaring)
	case ast.FieldDefinition:
		if n.Computed {
			field("Key", declaring)
		}
		field("Initializer", false)
	case ast.FunctionLiteral:
		field("Name", true)
		field("ParameterList", false)
		field("Body", false)
	case ast.ClassLiteral:
		field("Name", true)
		field("SuperClass", false)
		field("Body", false)
	default:
		for i := range v.NumField() {
			if v.Type().Field(i).Name != "DeclarationList" { // what a function declares, once more
				w.walk(v.Field(i), declaring)
			}
		}
	}
}

// name takes a name of the script: one it declares, or one it refers to,
// with where it is.
func (w *names) name(id ast.Identifier, declaring bool) {
	name := id.Name.String()
	if declaring {
		w.declared = appendNew(w.declared, name)
		return
	}
	// A name written with escapes is not where its letters are.
	if at := int(id.Idx) - 1; strings.HasPrefix(w.script[at:], name) {
		w.refs = append(w.refs, nameAt{name, at})
	}
}

// appendNew appends name to names unless it is in them.
func appendNew(names []string, name string) []string {
	if slices.Contains(names, name) {
		return names
	}
	return append(names, name)
}
