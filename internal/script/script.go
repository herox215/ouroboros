// Package script runs a script an LLM wrote: JavaScript, in an interpreter
// that is part of ouro (goja). A script gets the language and its standard
// objects, and the tools of the agent it runs for as functions. Nothing else
// reaches outside: no network, no files, no processes, no modules, no timers.
package script

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"runtime/metrics"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"
)

// The limits of a script. Beyond one of them it is stopped.
const (
	MaxTime   = 60 * time.Second // from its start to its result
	MaxCalls  = 64               // calls of tools
	MaxSource = 64 << 10         // bytes of the script
	MaxResult = 64 << 10         // bytes of what it returns
	MaxDepth  = 1024             // calls of functions within each other
	MaxHeap   = 256 << 20        // bytes the heap of the process may grow by while it runs
)

// Changed by tests, which do not wait a minute or fill the memory.
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

// Run runs source, the body of a function, and returns what it returns: a
// string as it is, everything else as JSON. Inside the script every name in
// tools is a function that takes one object, the arguments of the tool, and
// returns its result; it is run with call, under the context of this script.
// A tool that fails throws an Error with the text of its error.
//
// The functions are also in the object "tools": a tool whose name is a word
// of the language, like delete, can only be called there, as
// tools.delete({…}). Written as delete({…}) it would be the operator, which
// does nothing and says so to nobody, so such a script is not run.
//
// Every script starts in an interpreter of its own, which is gone when Run
// returns.
func Run(ctx context.Context, source string, tools []string, call Call) (string, error) {
	if len(source) > MaxSource {
		return "", fmt.Errorf("the script has %d bytes: at most %d are run", len(source), MaxSource)
	}
	for _, name := range tools {
		if reserved(name) && regexp.MustCompile(`(^|[^.\w$])`+regexp.QuoteMeta(name)+`\s*\(\s*\{`).MatchString(source) {
			return "", fmt.Errorf("%s is a word of JavaScript, so %s({…}) does not call the tool: write tools.%s({…})", name, name, name)
		}
	}
	// Line 1 of the script is line 1 of the program, for the errors.
	program, err := goja.Compile("script", "(function(){"+source+"\n})()", false)
	if err != nil {
		return "", fmt.Errorf("the script is not valid JavaScript: %w", err)
	}
	// The limits as they are now: what watches the script may outlive Run.
	limits := limits{time: maxTime, heap: maxHeap}
	wait := grace
	ctx, cancel := context.WithTimeout(ctx, limits.time)
	defer cancel()

	vm := goja.New()
	vm.SetMaxCallStackSize(MaxDepth)
	// Taken before the script runs, which may put something else in their place.
	newError, _ := goja.AssertConstructor(vm.Get("Error"))
	stringify, _ := goja.AssertFunction(vm.Get("JSON").ToObject(vm).Get("stringify"))
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

	type outcome struct {
		result string
		err    error
	}
	done := make(chan outcome, 1)
	var stopped atomic.Bool
	go func() {
		value, err := vm.RunProgram(program)
		stopped.Store(true)
		if err != nil {
			done <- outcome{err: failure(err)}
			return
		}
		result, err := text(value, stringify)
		done <- outcome{result, err}
	}()
	go limits.watch(ctx, vm, &stopped)

	select {
	case o := <-done:
		return o.result, o.err
	case <-ctx.Done():
	}
	// The interpreter was told to stop, see watch.
	select {
	case o := <-done:
		return o.result, o.err
	case <-time.After(wait):
		return "", fmt.Errorf("the script was stopped: %w, and a tool it called did not end", limits.why(ctx))
	}
}

// reserved reports whether name is a word of the language, which cannot be
// the name of a function.
func reserved(name string) bool {
	_, err := goja.Compile("", "var "+name, false)
	return err != nil
}

// watch stops the script in vm when ctx ends or the heap has grown too far,
// and ends when the script has stopped.
func (l limits) watch(ctx context.Context, vm *goja.Runtime, stopped *atomic.Bool) {
	start := heap()
	tick := time.NewTicker(watchEvery)
	defer tick.Stop()
	for !stopped.Load() {
		select {
		case <-ctx.Done():
			vm.Interrupt(l.why(ctx))
			return
		case <-tick.C:
			if now := heap(); now > start && now-start > l.heap {
				vm.Interrupt(fmt.Errorf("it used more than %d MiB of memory", l.heap>>20))
				return
			}
		}
	}
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

// text is what the LLM gets of the value a script returned.
func text(value goja.Value, stringify goja.Callable) (string, error) {
	result := ""
	if s, ok := value.(goja.String); ok {
		result = s.String()
	} else {
		encoded, err := stringify(goja.Undefined(), value)
		switch {
		case err != nil:
			return "", fmt.Errorf("what the script returned cannot be written as JSON: %w", err)
		case goja.IsUndefined(value):
			return "(the script returned nothing)", nil
		case goja.IsUndefined(encoded):
			return "", errors.New("what the script returned is no data: return a string, or what JSON can hold")
		}
		result = encoded.String()
	}
	if len(result) > MaxResult {
		return "", fmt.Errorf("the script returned %d bytes: at most %d are handed on, so return less", len(result), MaxResult)
	}
	return result, nil
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

// position is where in its compiled form a script was, which the interpreter
// adds to a line and column.
var position = regexp.MustCompile(`\(\d+\)\)?$`)
