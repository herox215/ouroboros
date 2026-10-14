// Package script runs a script an LLM wrote: JavaScript, in an interpreter
// that is part of ouro (goja). A script gets the language and its standard
// objects, print, and the tools of the agent it runs for as functions.
// Nothing else reaches outside: no network, no files, no processes, no
// modules, no timers. The scripts of one conversation share an interpreter,
// see Session.
package script

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"runtime/metrics"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dop251/goja"
	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/parser"
)

// The limits of a script. Beyond one of them it is stopped.
const (
	MaxTime   = 10 * time.Minute // from its start to its result: a build or its tests may take a while
	MaxCalls  = 64               // calls of tools
	MaxSource = 64 << 10         // bytes of the script
	MaxResult = 32 << 10         // bytes of what it returns that are handed on
	MaxPrint  = 32 << 10         // bytes of what it prints that are handed on: a source file of usual size
	MaxDepth  = 1024             // calls of functions within each other
	MaxHeap   = 256 << 20        // bytes the heap of the process may grow by while it runs

	// MaxSessions is how many conversations keep what their scripts left.
	MaxSessions = 16

	// Tight is what is handed on of what a script prints, and of what it
	// returns, under a context given by Tighten.
	Tight = 4 << 10
	// maxPrinted is how many bytes of what a script printed are kept in
	// Printed when not all of it is handed on.
	maxPrinted = 4 << 20
)

// The names under which the interpreter keeps for the next script what a
// script printed, and what it returned, when not all of it was handed on.
const (
	Printed  = "printed"
	Returned = "returned"
)

type tightKey struct{}

// Tighten returns a context under which a script hands on at most Tight
// bytes of what it prints, and of what it returns.
func Tighten(ctx context.Context) context.Context {
	return context.WithValue(ctx, tightKey{}, true)
}

// Untighten returns a context under which a script hands on what it prints,
// and what it returns, as far as the limits allow, whatever Tighten made of
// ctx: for an answer of its own within the one that was tightened.
func Untighten(ctx context.Context) context.Context {
	return context.WithValue(ctx, tightKey{}, false)
}

// Changed by tests, which do not wait ten minutes or fill the memory.
var (
	maxTime = MaxTime
	maxHeap = uint64(MaxHeap)
	// grace is how long a script that was stopped may take to end. It ends
	// at once unless it is in a tool that does not stop when it is told to:
	// then its result is not waited for.
	grace = 2 * time.Second
	// watchEvery is how often the heap is looked at.
	watchEvery = 5 * time.Millisecond
)

// limits are the limits one script runs with.
type limits struct {
	time time.Duration
	heap uint64
}

// Call runs the tool called name with the arguments args, a JSON object, and
// returns the text the LLM would get of it.
type Call func(ctx context.Context, name string, args json.RawMessage) (string, error)

// Run runs source in an interpreter of its own, which is gone when Run
// returns: see Session.Run.
func Run(ctx context.Context, source string, tools []string, call Call) (string, error) {
	return NewSession().Run(ctx, source, tools, call)
}

// A Session runs the scripts of one conversation one after the other in one
// interpreter: what a script declares at its top level, or puts on
// globalThis, is there for the scripts after it. Its scripts may be started
// from more than one goroutine; each waits for the one before to end.
type Session struct {
	turn chan struct{} // holds the turn to run a script
	// Held by whoever has the turn: nil before the first script and after
	// one that broke it.
	realm *realm
}

// realm is an interpreter and what a session keeps of it. Its scripts may
// change the standard objects: the functions it calls are taken before
// any script ran.
type realm struct {
	vm        *goja.Runtime
	newError  goja.Constructor
	stringify goja.Callable
	describe  goja.Callable // Object.getOwnPropertyDescriptor
	isArray   goja.Callable
	keys      goja.Callable   // Object.keys
	get       goja.Callable   // Reflect.get
	standard  map[string]bool // what is on globalThis before a script ran, and what every script gets
	installed []string        // the names of the tools the last script had
}

// NewSession returns a session that has run nothing yet.
func NewSession() *Session {
	s := &Session{turn: make(chan struct{}, 1)}
	s.turn <- struct{}{}
	return s
}

// Run runs source and returns what it gives back: what it printed, and
// after that the value of its last statement, a string as it is, everything
// else as JSON. A source that returns at its top level is the body of a
// function instead, whose value is what it returns: its declarations end
// with it.
//
// Inside the script every name in tools is a function that takes one object,
// the arguments of the tool, and returns its result; it is run with call,
// under the context of this script. A tool that fails throws an Error with
// the text of its error. print (and console.log) adds a line to what the
// script gives back.
//
// The functions are also in the object "tools": a tool whose name is a word
// of the language, like delete, can only be called there, as
// tools.delete({…}). Written as delete({…}) it would be the operator, which
// does nothing and says so to nobody, so such a script is not run.
//
// A script that was stopped because of its memory, or that left a tool
// behind that did not end, takes the interpreter with it: the next one starts
// in a new one.
func (s *Session) Run(ctx context.Context, source string, tools []string, call Call) (string, error) {
	if len(source) > MaxSource {
		return "", fmt.Errorf("the script has %d bytes: at most %d are run", len(source), MaxSource)
	}
	for _, name := range tools {
		if reserved(name) && regexp.MustCompile(`(^|[^.\w$])`+regexp.QuoteMeta(name)+`\s*\(\s*\{`).MatchString(source) {
			return "", fmt.Errorf("%s is a word of JavaScript, so %s({…}) does not call the tool: write tools.%s({…})", name, name, name)
		}
	}
	program, err := compile(source, tools)
	if err != nil && awaits.MatchString(source) {
		// The tools return their result right away: an await before one
		// changes nothing but keeps the script from being one.
		if without, again := compile(unawaited(source), tools); again == nil {
			program, err = without, nil
		}
	}
	if err != nil {
		return "", fmt.Errorf("the script is not valid JavaScript: %w", err)
	}
	select {
	case <-s.turn:
	case <-ctx.Done():
		return "", errors.New("the script was not run: the conversation was cancelled")
	}
	if s.realm == nil {
		s.realm = newRealm()
	}
	r := s.realm
	vm := r.vm
	// The limits as they are now: what watches the script may outlive Run.
	limits := limits{time: maxTime, heap: maxHeap}
	wait := grace
	handed := MaxPrint
	returned := MaxResult
	if tight, _ := ctx.Value(tightKey{}).(bool); tight {
		handed, returned = min(handed, Tight), min(returned, Tight)
	}
	ctx, cancel := context.WithTimeout(ctx, limits.time)
	defer cancel()
	out := &output{limit: handed}
	r.install(ctx, tools, call, out, limits)

	type outcome struct {
		result  string
		nothing bool   // the script gave back no value
		kept    string // see realm.kept
		err     error  // of the script
		value   error  // of what it gave back
	}
	done := make(chan outcome, 1)
	ended := make(chan struct{})
	watched := make(chan struct{})
	go func() {
		// What it gave back is read under the limits too: it may be an
		// object whose methods are part of the script.
		var o outcome
		value, err := vm.RunProgram(program)
		if err == nil {
			value, err = settled(value)
		}
		if o.err = err; err == nil {
			var cut bool
			o.result, o.nothing, cut, o.value = text(value, r.stringify, returned)
			if cut {
				vm.Set(Returned, value)
			}
		}
		if out.over() {
			vm.Set(Printed, out.all.String())
		}
		if o.err == nil && o.value == nil {
			o.kept = r.kept()
		}
		close(ended)
		done <- o
	}()
	go func() {
		limits.watch(ctx, vm, ended)
		close(watched)
	}()

	var o outcome
	select {
	case o = <-done:
	case <-ctx.Done():
		// The interpreter was told to stop, see watch.
		select {
		case o = <-done:
		case <-time.After(wait):
			// It is still in use: the next script gets another one.
			s.realm = nil
			s.turn <- struct{}{}
			return "", fmt.Errorf("the script was stopped: %w, and a tool it called did not end%s", limits.why(ctx), anew)
		}
	}
	// What watches the script may have told it to stop after it ended.
	<-watched
	vm.ClearInterrupt()
	var memory memoryError
	if errors.As(o.err, &memory) || errors.As(o.value, &memory) {
		s.realm = nil
	}
	s.turn <- struct{}{}
	switch {
	case o.err != nil:
		return "", printed(known(failure(o.err), tools, source), out)
	case o.value != nil:
		return "", printed(o.value, out)
	}
	var parts []string
	if out.size > 0 {
		parts = append(parts, out.String())
	}
	if !o.nothing || len(parts) == 0 {
		parts = append(parts, o.result)
	}
	if o.kept != "" {
		parts = append(parts, o.kept)
	}
	return strings.Join(parts, "\n"), nil
}

// anew is what the error of a script that took its interpreter with it says.
const anew = ": what the scripts before it kept is gone"

// newRealm returns a new interpreter.
func newRealm() *realm {
	vm := goja.New()
	vm.SetMaxCallStackSize(MaxDepth)
	function := func(object, name string) goja.Callable {
		f, _ := goja.AssertFunction(vm.Get(object).ToObject(vm).Get(name))
		return f
	}
	r := &realm{vm: vm, standard: map[string]bool{"tools": true, "print": true, "console": true}}
	r.newError, _ = goja.AssertConstructor(vm.Get("Error"))
	r.stringify = function("JSON", "stringify")
	r.describe = function("Object", "getOwnPropertyDescriptor")
	r.keys = function("Object", "keys")
	r.isArray = function("Array", "isArray")
	r.get = function("Reflect", "get")
	for _, name := range vm.GlobalObject().GetOwnPropertyNames() {
		r.standard[name] = true
	}
	return r
}

// maxKept is how many names the line of what is kept lists.
const maxKept = 30

// kept is the line that tells the LLM what the interpreter keeps for the
// next script: the names a script put on globalThis, with what they are.
// "" if there is none. It runs in the interpreter, under the limits of the
// script that just ended.
func (r *realm) kept() string {
	global := r.vm.GlobalObject()
	var names []string
	for _, name := range global.GetOwnPropertyNames() {
		if !r.standard[name] && !slices.Contains(r.installed, name) {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	var parts []string
	for _, name := range names[:min(len(names), maxKept)] {
		parts = append(parts, name+" ("+r.what(global, name)+")")
	}
	if len(names) > maxKept {
		parts = append(parts, fmt.Sprintf("and %d more", len(names)-maxKept))
	}
	return "[kept for the next script: " + strings.Join(parts, ", ") + "]"
}

// what says what the property name of global is, without calling a getter
// the script put there. What a Proxy makes of being looked at runs under the
// limits of the script.
func (r *realm) what(global *goja.Object, name string) string {
	d, _ := r.describe(goja.Undefined(), global, r.vm.ToValue(name)) // globalThis is no Proxy: cannot fail
	descriptor := d.ToObject(r.vm)
	if descriptor.Get("get") != nil && !goja.IsUndefined(descriptor.Get("get")) {
		return "getter"
	}
	value := descriptor.Get("value")
	switch v := value.(type) {
	case goja.String:
		return fmt.Sprintf("string of %d bytes", len(v.String()))
	case *goja.Object:
		if _, ok := goja.AssertFunction(v); ok {
			return "function"
		}
		if array, err := r.isArray(goja.Undefined(), v); err == nil && array.ToBoolean() {
			if length, err := r.get(goja.Undefined(), v, r.vm.ToValue("length")); err == nil {
				return fmt.Sprintf("array of %d", length.ToInteger())
			}
			return "array"
		}
		if keys, err := r.keys(goja.Undefined(), v); err == nil {
			return fmt.Sprintf("object with %d keys", len(keys.Export().([]any)))
		}
		return "object"
	}
	if goja.IsNull(value) || goja.IsUndefined(value) {
		return value.String()
	}
	switch value.ExportType().Kind() {
	case reflect.Bool:
		return "boolean"
	case reflect.Int64, reflect.Float64:
		return "number"
	}
	if _, ok := value.(*goja.Symbol); ok {
		return "symbol"
	}
	return "bigint"
}

// install gives the interpreter the tools and print for one script, which
// runs under ctx and prints to out. They take the place of those the script
// before had, whatever it made of them.
func (r *realm) install(ctx context.Context, tools []string, call Call, out *output, limits limits) {
	vm := r.vm
	for _, name := range r.installed {
		if !slices.Contains(tools, name) {
			vm.GlobalObject().Delete(name)
		}
	}
	r.installed = slices.Clone(tools)
	newError, stringify := r.newError, r.stringify
	throw := func(err error) {
		thrown, _ := newError(nil, vm.ToValue(err.Error()))
		panic(thrown) // goja throws a value it is panicked with
	}
	calls := 0
	object := vm.NewObject()
	vm.Set("tools", object)
	for _, name := range tools {
		function := vm.ToValue(func(args goja.FunctionCall) goja.Value {
			raw := json.RawMessage("{}")
			if arg := args.Argument(0); !goja.IsUndefined(arg) {
				text, err := stringify(goja.Undefined(), arg)
				if err != nil || goja.IsUndefined(text) {
					throw(fmt.Errorf("%s takes one object with its arguments", name))
				}
				raw = json.RawMessage(text.String())
			}
			if calls++; calls > MaxCalls {
				vm.Interrupt(fmt.Errorf("it called more than %d tools", MaxCalls))
				return goja.Undefined()
			}
			result, err := call(ctx, name, raw)
			// A script whose time ended while the tool ran is over, whatever
			// the tool made of being told so.
			if ctx.Err() != nil {
				vm.Interrupt(limits.why(ctx))
				return goja.Undefined()
			}
			if err != nil {
				throw(err)
			}
			return vm.ToValue(result)
		})
		object.Set(name, function)
		vm.Set(name, function)
	}
	print := vm.ToValue(func(args goja.FunctionCall) goja.Value {
		parts := make([]string, len(args.Arguments))
		for i, arg := range args.Arguments {
			parts[i] = show(arg, stringify)
		}
		out.add(strings.Join(parts, " "))
		return goja.Undefined()
	})
	console := vm.NewObject()
	for _, name := range []string{"log", "info", "warn", "error"} {
		console.Set(name, print)
	}
	vm.Set("print", print)
	vm.Set("console", console)
}

// awaits finds an await in a script. A script is no async function, so an
// await in it is a syntax error, which the models read as anything but that.
var awaits = regexp.MustCompile(`(^|[^.\w$])await\b`)

// unawaited returns source with every await and async that is a word of its
// own, outside strings and comments, made blanks: lines and columns stay
// where they were, for the errors. Without async, a function of the script
// returns what it returns, not a promise of it, as the tools do.
func unawaited(source string) string {
	code := []byte(source)
	var quote byte // the quote of the string the scan is in, if any
	for i := 0; i < len(code); i++ {
		c := code[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'' || c == '`':
			quote = c
		case c == '/' && i+1 < len(code) && code[i+1] == '/':
			for i < len(code) && code[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(code) && code[i+1] == '*':
			end := bytes.Index(code[i+2:], []byte("*/"))
			if end < 0 {
				return string(code)
			}
			i += end + 3
		case keyword(code, i):
			copy(code[i:], "     ")
			i += len("await") - 1
		}
	}
	return string(code)
}

// keyword reports whether await or async, which are as long, is a word of
// its own at i of code.
func keyword(code []byte, i int) bool {
	end := i + len("await")
	return (bytes.HasPrefix(code[i:], []byte("await")) || bytes.HasPrefix(code[i:], []byte("async"))) &&
		(i == 0 || !word(code[i-1])) && (end == len(code) || !word(code[end]))
}

// word reports whether c is part of a name in JavaScript.
func word(c byte) bool {
	return c == '_' || c == '$' || c == '.' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

// compile returns the program of source. Its declarations at the top level
// are those of var, so that the next script may declare them again. A call
// of one of tools that is a statement of its own at the top level, and not
// the last one, prints what it returns: its result would be lost otherwise,
// as only the value of the last statement comes back. The columns of its
// line after the call are further on in an error then, by the length of
// shown.
func compile(source string, tools []string) (*goja.Program, error) {
	parsed, err := parser.ParseFile(nil, "script", source, 0)
	if err != nil {
		if !strings.Contains(err.Error(), "Illegal return statement") {
			_, err = goja.Compile("script", source, false) // with the kind of the error
			return nil, err
		}
		// Line 1 of the script is line 1 of the program, for the errors.
		return goja.Compile("script", "(function(){"+source+"\n})()", false)
	}
	code := []byte(source)
	var lost []ast.Expression // calls of tools whose result is lost
	for i, statement := range parsed.Body {
		switch s := statement.(type) {
		case *ast.LexicalDeclaration:
			at := int(s.Idx) - 1
			word := s.Token.String() // let or const
			copy(code[at:], "var"+strings.Repeat(" ", len(word)-len("var")))
		case *ast.ExpressionStatement:
			if i < len(parsed.Body)-1 && callsTool(s.Expression, tools) {
				lost = append(lost, s.Expression)
			}
		}
	}
	for _, call := range slices.Backward(lost) {
		from, to := int(call.Idx0())-1, int(call.Idx1())-1
		code = slices.Concat(code[:from], []byte(shown), code[from:to], []byte(")"), code[to:])
	}
	return goja.Compile("script", string(code), false)
}

// shown is what compile puts before a call whose result would be lost: it
// prints the result unless it is empty, without a name of its own that a
// script would see.
const shown = `((r) => r === "" || print(r))(`

// callsTool reports whether e is a call of one of tools, by its name or as
// tools.NAME.
func callsTool(e ast.Expression, tools []string) bool {
	call, ok := e.(*ast.CallExpression)
	if !ok {
		return false
	}
	switch callee := call.Callee.(type) {
	case *ast.Identifier:
		return slices.Contains(tools, callee.Name.String())
	case *ast.DotExpression:
		object, ok := callee.Left.(*ast.Identifier)
		return ok && object.Name == "tools" && slices.Contains(tools, callee.Identifier.Name.String())
	}
	return false
}

// output is what a script printed.
type output struct {
	all   strings.Builder // what was printed, up to maxPrinted bytes
	size  int             // bytes that were printed
	limit int             // bytes that are handed on
}

// add adds a line.
func (o *output) add(line string) {
	line += "\n"
	o.size += len(line)
	if room := maxPrinted - o.all.Len(); room > 0 {
		o.all.WriteString(head(line, room))
	}
}

// over reports whether more was printed than is handed on.
func (o *output) over() bool { return o.size > o.limit }

// String is what was printed as far as it is handed on, with how much of it
// was left out and where all of it is kept.
func (o *output) String() string {
	all := o.all.String()
	if !o.over() {
		return strings.TrimSuffix(all, "\n")
	}
	shown := head(all, o.limit)
	// Whole lines, unless that leaves out more than half of what fits.
	if end := strings.LastIndexByte(shown, '\n'); end >= o.limit/2 {
		shown = shown[:end+1]
	}
	left := o.size - len(shown)
	if !strings.HasSuffix(shown, "\n") {
		shown += "\n"
	}
	kept := "all of it"
	if o.size > maxPrinted {
		kept = fmt.Sprintf("the first %d MiB of it", maxPrinted>>20)
	}
	return fmt.Sprintf("%s(%d more bytes were printed and left out. %s is kept in %s, a string: "+
		"look into it, like %s.indexOf(…) or %s.slice(…), and print only what you need)",
		shown, left, upper(kept), Printed, Printed, Printed)
}

// upper is s with its first letter in upper case.
func upper(s string) string { return strings.ToUpper(s[:1]) + s[1:] }

// head is at most n bytes from the start of s, ending where a character
// begins.
func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// printed is err with what the script printed before it.
func printed(err error, out *output) error {
	if out.size == 0 {
		return err
	}
	return fmt.Errorf("%w\nIt printed before:\n%s", err, out)
}

// show is a value as print writes it: a string as it is, everything else as
// JSON where it can be.
func show(value goja.Value, stringify goja.Callable) string {
	if s, ok := value.(goja.String); ok {
		return s.String()
	}
	if encoded, err := stringify(goja.Undefined(), value); err == nil && !goja.IsUndefined(encoded) {
		return encoded.String()
	}
	return value.String()
}

// Reserved reports whether name is a word of the language, which cannot be
// the name of a function: a tool of that name is called as tools.NAME.
func Reserved(name string) bool { return reserved(name) }

// reserved reports whether name is a word of the language, which cannot be
// the name of a function.
func reserved(name string) bool {
	_, err := goja.Compile("", "var "+name, false)
	return err != nil
}

// watch stops the script in vm when ctx ends or the heap has grown too far,
// and returns when the script has ended.
func (l limits) watch(ctx context.Context, vm *goja.Runtime, ended <-chan struct{}) {
	start := heap()
	tick := time.NewTicker(watchEvery)
	defer tick.Stop()
	for {
		select {
		case <-ended:
			return
		case <-ctx.Done():
			vm.Interrupt(l.why(ctx))
			return
		case <-tick.C:
			if now := heap(); now > start && now-start > l.heap {
				vm.Interrupt(memoryError{l.heap})
				return
			}
		}
	}
}

// memoryError stops a script that used more memory than limit.
type memoryError struct{ limit uint64 }

func (e memoryError) Error() string {
	return fmt.Sprintf("it used more than %d MiB of memory%s", e.limit>>20, anew)
}

// why says what ended ctx.
func (l limits) why(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("it ran for more than %s", l.time)
	}
	return errors.New("the conversation was cancelled")
}

// heap is the size of the objects on the heap of the process, the garbage
// that was not collected yet included.
func heap() uint64 {
	sample := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	metrics.Read(sample)
	return sample[0].Value.Uint64()
}

// text is what the LLM gets of the value a script gave back, whether that is
// nothing, and whether it was cut: at most limit bytes of it are handed on,
// with a line that says the value is kept in Returned.
func text(value goja.Value, stringify goja.Callable, limit int) (string, bool, bool, error) {
	result := ""
	if s, ok := value.(goja.String); ok {
		result = s.String()
	} else {
		encoded, err := stringify(goja.Undefined(), value)
		switch {
		case err != nil:
			return "", false, false, fmt.Errorf("what the script returned cannot be written as JSON: %w", err)
		case goja.IsUndefined(value):
			return "(the script returned nothing)", true, false, nil
		case goja.IsUndefined(encoded):
			return "", false, false, errors.New("what the script returned is no data: return a string, or what JSON can hold")
		}
		result = encoded.String()
	}
	if len(result) > limit {
		shown := head(result, limit)
		return fmt.Sprintf("%s\n(the value has %d bytes, of which the first %d came back. It is kept in %s: "+
			"look into it and give back only what you need)", shown, len(result), len(shown), Returned), false, true, nil
	}
	return result, false, false, nil
}

// settled is what a script gave back that is a promise, as an async
// function returns one: what it was fulfilled with, or why it was rejected,
// as if the script had thrown it. The interpreter has run what the promises
// waited for once the script ended.
func settled(value goja.Value) (goja.Value, error) {
	p, ok := value.Export().(*goja.Promise)
	if !ok {
		return value, nil
	}
	switch p.State() {
	case goja.PromiseStateFulfilled:
		return p.Result(), nil
	case goja.PromiseStateRejected:
		reason := p.Result().String()
		if obj, ok := p.Result().(*goja.Object); ok {
			if stack := obj.Get("stack"); stack != nil && !goja.IsUndefined(stack) {
				reason = stack.String()
			}
		}
		return nil, errors.New(reason)
	}
	return nil, errors.New("the script gave back a promise that never settled: the tools are functions that " +
		"return their result at once, so call them without await, and return the result itself")
}

// failure is the error of a script that did not get to its end, as the LLM
// reads it: with the line of the script it happened in.
func failure(err error) error {
	var (
		thrown      *goja.Exception
		interrupted *goja.InterruptedError
		overflow    *goja.StackOverflowError
	)
	switch {
	case errors.As(err, &interrupted):
		return fmt.Errorf("the script was stopped: %w", errors.Unwrap(interrupted))
	case errors.As(err, &overflow):
		return fmt.Errorf("the script was stopped: more than %d calls within each other,%s", MaxDepth, overflow.Error())
	}
	// What the script threw and did not catch, with where it was: the lines
	// of the script, without what only the interpreter knows.
	message := err.Error()
	if errors.As(err, &thrown) {
		message = thrown.String()
	}
	var lines []string
	for line := range strings.Lines(message) {
		if line = strings.TrimRight(line, "\n"); !strings.HasSuffix(line, "(native)") {
			lines = append(lines, position.ReplaceAllString(line, ""))
		}
	}
	return fmt.Errorf("the script failed: %s", strings.Join(lines, "\n"))
}

// undefined finds the name a script used that is defined nowhere.
var undefined = regexp.MustCompile(`ReferenceError: ([\p{L}\p{N}_$]+) is not defined`)

// known adds to the error of a script that called a function defined
// nowhere the functions it has for its tools: a model guesses one, like
// readFile for read.
func known(err error, tools []string, source string) error {
	m := undefined.FindStringSubmatch(err.Error())
	if m == nil || len(tools) == 0 || !strings.Contains(source, m[1]+"(") {
		return err
	}
	return fmt.Errorf("%w\n[the functions for the tools are: %s]", err, strings.Join(tools, ", "))
}

// position is where in its compiled form a script was, which the interpreter
// adds to a line and column.
var position = regexp.MustCompile(`\(\d+\)\)?$`)

// Sessions are the sessions of the conversations of an agent, by their ID.
// Of more than MaxSessions the one that ran a script the longest ago is
// forgotten, with all its scripts kept.
type Sessions struct {
	mu       sync.Mutex
	clock    uint64
	sessions map[string]*kept
}

type kept struct {
	session *Session
	used    uint64
}

// NewSessions returns sessions of no conversation yet.
func NewSessions() *Sessions {
	return &Sessions{sessions: map[string]*kept{}}
}

// Of returns the session of the conversation id, a new one if there is none.
func (s *Sessions) Of(id string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clock++
	k, ok := s.sessions[id]
	if !ok {
		k = &kept{session: NewSession()}
		s.sessions[id] = k
	}
	k.used = s.clock
	if len(s.sessions) > MaxSessions {
		oldest := id
		for other, k := range s.sessions {
			if k.used < s.sessions[oldest].used {
				oldest = other
			}
		}
		delete(s.sessions, oldest)
	}
	return k.session
}
