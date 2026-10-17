// A service is a command that keeps running past the answer that started
// it, like a server the owner looks at in the browser: it ends when it does,
// when it is stopped, when its time is up, or when the node stops, and
// nothing of it outlives the node. Every service belongs to whom it runs
// for, the owner or a peer, who alone sees and stops it through the agent;
// the owner sees and stops all of them in the front end.
//
// What a service starts is ended with it in three ways, so that nothing of
// it is left running:
//
//   - It runs as a group of processes of its own, which is ended as one.
//   - What it starts is followed in the table of the processes while it
//     runs, so that what left the group, with setsid, is ended too (see
//     family).
//   - It watches a pipe the node holds: once the node is gone, also when it
//     was killed or crashed, the pipe closes, and the service ends its group
//     itself. What left the group is noted in the file of the services, and
//     ended when the node starts again (see supervisor.start).

package shell

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"ouroboros/internal/guard"
	"ouroboros/internal/safefile"
	"ouroboros/plugin"
)

// watchEnv names the variable that says which descriptor of a service is
// the end of the pipe the node holds, see serviceScript.
const watchEnv = "OURO_WATCH_FD"

// serviceScript runs the command of a service, its first argument, and ends
// with it, with its exit status. Next to it a watcher waits for the pipe of
// the node to close, which happens once the node is gone, and then ends the
// whole group. Neither the command gets the pipe, nor the watcher the output,
// which it would keep open past the end of the command. Asked to end, the script
// waits for the command to end, so that it is killed only once its time to
// end is over.
const serviceScript = `f=$` + watchEnv + `
eval "( read -r _ <&$f; kill -KILL 0 ) >/dev/null 2>&1 &"
eval "bash -c \"\$1\" ouro-service $f<&- &"
c=$!
trap 'kill -TERM $c 2>/dev/null' TERM
wait $c
s=$?
while kill -0 $c 2>/dev/null; do wait $c; s=$?; done
exit $s`

// Limits of the services.
const (
	// MaxServices is how many services of the owner run at once,
	// MaxPeerServices how many of each peer.
	MaxServices     = 4
	MaxPeerServices = 2
	// How long a service of the owner runs if not told, and at most; and
	// those of a peer.
	ServiceTime        = 8 * time.Hour
	MaxServiceTime     = 24 * time.Hour
	PeerServiceTime    = time.Hour
	MaxPeerServiceTime = 4 * time.Hour
	// serviceKept is how long one that ended is still told of.
	serviceKept = time.Hour
	// serviceGrace is how long a service has to end once it is asked to,
	// before it is killed.
	serviceGrace = 3 * time.Second
	// readyTime is how long the start waits for the port of a service.
	readyTime = 15 * time.Second
	// serviceLog is how much of what a service printed last is kept.
	serviceLog = 64 << 10
)

// serviceName is what a service may be named.
var serviceName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// serviceStart is what launch starts a service with.
type serviceStart struct {
	tag   string
	watch *os.File // the end of the pipe the service watches
	log   *ring
}

// service is a command that runs past the answer, see the package comment.
type service struct {
	tag, name string
	peer      string // the public key of the peer it runs for, "" for the owner
	command   string
	port      int
	p         *process
	family    *family  // its processes
	watch     *os.File // the end of the pipe the node holds
	log       *ring
	until     time.Time
	timer     *time.Timer

	// why it was stopped, "" while nobody stopped it; and when it ended,
	// zero while it runs
	why   string
	ended time.Time
}

// running reports whether the service still runs.
func (sv *service) running() bool {
	select {
	case <-sv.p.done:
		return false
	default:
		return true
	}
}

// supervisor holds the services of a profile of the node.
type supervisor struct {
	file string // where those that run are noted, for a node that ends without stopping them
	logf func(format string, args ...any)

	mu       sync.Mutex
	all      []*service
	closed   bool // the node stopped: no service starts any more
	tracking bool // the processes of the services are followed, see track
}

// record is a service as the file of the services notes it.
type record struct {
	Tag     string    `json:"tag"`
	Name    string    `json:"name"`
	Command string    `json:"command"`
	Since   time.Time `json:"since"`
	Group   int       `json:"group"`
	Members []member  `json:"members"`
}

// start ends what a node that ended without stopping its services left of
// them, as the node starts.
func (sv *supervisor) start() {
	data, err := os.ReadFile(sv.file)
	if err != nil {
		return
	}
	var left []record
	json.Unmarshal(data, &left)
	for _, r := range left {
		// The group is not killed: its number may be another's by now. Its
		// members are known by when they started too.
		if n := newFamily(r.Tag, 0, r.Members...).end(); n > 0 {
			sv.logf("service %s of a node before was still running (%d processes): stopped", r.Name, n)
		}
	}
	sv.mu.Lock()
	defer sv.mu.Unlock()
	sv.note()
}

// stopAll stops every service, as the node stops, and starts none any more.
func (sv *supervisor) stopAll() {
	sv.mu.Lock()
	sv.closed = true
	running := slices.DeleteFunc(slices.Clone(sv.all), func(s *service) bool { return !s.running() })
	sv.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range running {
		wg.Go(func() { sv.stop(s, "the node stopped") })
	}
	wg.Wait()
	sv.mu.Lock()
	defer sv.mu.Unlock()
	sv.note()
}

// note writes the services that run to the file of the services. sv.mu is
// held.
func (sv *supervisor) note() {
	var records []record
	for _, s := range sv.all {
		if s.running() {
			records = append(records, record{s.tag, s.name, firstLine(s.command), s.p.start, s.family.pgid, s.family.list()})
		}
	}
	if len(records) == 0 {
		os.Remove(sv.file)
		return
	}
	data, _ := json.MarshalIndent(records, "", "  ")
	if err := safefile.Write(sv.file, data, 0o600); err != nil {
		sv.logf("the services that run cannot be noted: %v", err)
	}
}

// launch starts the service name of peer with command in dir, as start
// prepared it, and waits for its port.
func (sv *supervisor) launch(name, peer, command, dir string, port int, d time.Duration, s settings) (*service, error) {
	r, w, err := pipe()
	if err != nil {
		return nil, err
	}
	defer r.Close() // the service has its own copy
	tag := newTag()
	start := &serviceStart{tag: tag, watch: r, log: &ring{}}
	ctx, cancel := context.WithCancel(context.Background())
	p, err := launch(ctx, cancel, dir, s, command, start)
	if err != nil {
		w.Close()
		cancel()
		return nil, err
	}
	svc := &service{tag: tag, name: name, peer: peer, command: command, port: port, p: p, watch: w, log: start.log, until: p.start.Add(d),
		family: newFamily(tag, p.cmd.Process.Pid)}
	svc.family.grow(procs())
	sv.mu.Lock()
	sv.all = append(sv.all, svc)
	sv.note()
	if !sv.tracking {
		sv.tracking = true
		go sv.track()
	}
	sv.mu.Unlock()
	svc.timer = time.AfterFunc(d, func() { sv.stop(svc, "its time was up") })
	go sv.ended(svc)
	return svc, nil
}

// ended waits for the service to end, by itself or stopped, and ends what
// is left of it.
func (sv *supervisor) ended(s *service) {
	<-s.p.done
	s.timer.Stop()
	// Its group, what left it, and the watcher, which ends the group
	// itself once its pipe closes.
	s.family.end()
	s.watch.Close()
	sv.mu.Lock()
	defer sv.mu.Unlock()
	s.ended = time.Now()
	sv.note()
}

// track follows the processes of the services in the table of the
// processes, as long as one runs, and notes them.
func (sv *supervisor) track() {
	for {
		time.Sleep(trackEvery)
		sv.mu.Lock()
		var running []*service
		for _, s := range sv.all {
			if s.running() {
				running = append(running, s)
			}
		}
		if len(running) == 0 {
			sv.tracking = false
			sv.mu.Unlock()
			return
		}
		sv.mu.Unlock()
		table := procs()
		changed := false
		for _, s := range running {
			if s.family.grow(table) {
				changed = true
			}
		}
		if changed {
			sv.mu.Lock()
			sv.note()
			sv.mu.Unlock()
		}
	}
}

// stop stops the service: it is asked to end, and killed with everything
// it started after serviceGrace. why is what the agent is told of it. It
// returns once the service ended.
func (sv *supervisor) stop(s *service, why string) {
	sv.mu.Lock()
	if s.why == "" && s.running() {
		s.why = why
	}
	sv.mu.Unlock()
	if s.running() {
		syscall.Kill(-s.p.cmd.Process.Pid, syscall.SIGTERM)
	}
	select {
	case <-s.p.done:
	case <-time.After(serviceGrace):
		s.p.cancel()
		<-s.p.done
	}
	// ended has cleaned up, or does so now
	for {
		sv.mu.Lock()
		over := !s.ended.IsZero()
		sv.mu.Unlock()
		if over {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// find is the service name of peer: the one that runs, or the last one
// that ended. sv.mu is held.
func (sv *supervisor) find(peer, name string) *service {
	var found *service
	for _, s := range sv.all {
		if s.peer == peer && s.name == name && (found == nil || s.running() || !found.running()) {
			found = s
		}
	}
	return found
}

// forget drops what ended longer than serviceKept ago. sv.mu is held.
func (sv *supervisor) forget() {
	sv.all = slices.DeleteFunc(sv.all, func(s *service) bool {
		return !s.ended.IsZero() && time.Since(s.ended) > serviceKept
	})
}

// list are the services that run, for the front ends.
func (sv *supervisor) list() []plugin.ServiceInfo {
	sv.mu.Lock()
	defer sv.mu.Unlock()
	var list []plugin.ServiceInfo
	for _, s := range sv.all {
		if s.running() {
			list = append(list, plugin.ServiceInfo{ID: s.tag, Name: s.name, PeerKey: s.peer, Command: firstLine(s.command),
				Port: s.port, Since: s.p.start, Until: s.until})
		}
	}
	return list
}

// stopID stops the service with the tag id, for the owner in a front end,
// and reports whether it ran.
func (sv *supervisor) stopID(id string) bool {
	sv.mu.Lock()
	i := slices.IndexFunc(sv.all, func(s *service) bool { return s.tag == id && s.running() })
	var s *service
	if i >= 0 {
		s = sv.all[i]
	}
	sv.mu.Unlock()
	if s == nil {
		return false
	}
	sv.stop(s, "the owner stopped it")
	return true
}

// newTag is a tag no other service has.
func newTag() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ToolService is the tool for services.
const ToolService = "service"

type serviceArgs struct {
	Action  string  `json:"action"`
	Name    string  `json:"name"`
	Command string  `json:"command"`
	Dir     string  `json:"dir"`
	Port    int     `json:"port"`
	Hours   float64 `json:"hours"`
	Lines   int     `json:"lines"`
}

// ServiceTool returns the tool for the services of sv, which asks g where
// one may start.
func ServiceTool(sv *supervisor, g *guard.Guard) plugin.Tool {
	t := plugin.Tool{
		Name: ToolService,
		Description: "Keep a command running past this answer, as a service: a server, a watcher, a mock API the user or you use later. " +
			"Not for builds, tests or what is needed only while you answer: bash runs those, with \"background\": true for a server you test against now. " +
			"A service runs as bash does, in no sandbox: a program with windows opens on the screen. " +
			"It ends when its command ends, when it is stopped, when its time is up, or when the node stops or is rebuilt: never longer. " +
			"Nothing it starts outlives it, also with &, nohup or setsid: do not try to keep anything running another way. " +
			"Tell the user so, and when it ends, and do not say it runs without looking with \"status\". " +
			`{"action": "start", "name": "web", "command": "npm run dev", "port": 5173} starts one and returns once its port answers, with what it printed; ` +
			`{"action": "status", "name": "web"} says whether it runs, with its last lines; {"action": "logs", "name": "web", "lines": 100} shows more of them; ` +
			`{"action": "stop", "name": "web"} stops it with everything it started; {"action": "list"} lists the services of this conversation's owner. ` +
			fmt.Sprintf("The owner's services run %s if not told otherwise, at most %s, and at most %d at once; a peer's %s, at most %s, and %d at once.",
				hours(ServiceTime), hours(MaxServiceTime), MaxServices, hours(PeerServiceTime), hours(MaxPeerServiceTime), MaxPeerServices),
		Schema: `{"type":"object","properties":{` +
			`"action":{"type":"string","enum":["start","status","logs","stop","list"],"description":"what to do"},` +
			`"name":{"type":"string","description":"the name of the service: small letters, digits and -, starting with a letter; needed but with list"},` +
			`"command":{"type":"string","description":"with start: the command, as bash -c takes it"},` +
			`"dir":{"type":"string","description":"with start: the directory it runs in, as with bash"},` +
			`"port":{"type":"integer","description":"with start: the port on localhost it listens on, which start waits for"},` +
			`"hours":{"type":"number","description":"with start: how many hours it may run"},` +
			`"lines":{"type":"integer","description":"with logs: how many of its last lines, 40 if missing"}` +
			`},"required":["action"]}`,
		Trusted: true,
	}
	return plugin.Configurable(t, func(s settings) (plugin.RunFunc, error) {
		for _, name := range s.Env {
			if name == "" || strings.ContainsAny(name, "= ") {
				return nil, fmt.Errorf("%q in env is no name of a variable", name)
			}
		}
		return func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a serviceArgs
			if err := plugin.Decode(ToolService, raw, &a); err != nil {
				return "", err
			}
			return sv.run(ctx, g, s, a)
		}, nil
	})
}

// hours is d as the tool tells it.
func hours(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%d minutes", int(d/time.Minute))
	}
	if d == time.Hour {
		return "1 hour"
	}
	return fmt.Sprintf("%d hours", int(d/time.Hour))
}

// run does what a asks for, for whom the conversation of ctx is.
func (sv *supervisor) run(ctx context.Context, g *guard.Guard, s settings, a serviceArgs) (string, error) {
	origin, isPeer := plugin.OriginFrom(ctx)
	peer := ""
	if isPeer {
		peer = origin.PublicKey
	}
	if a.Action == "list" {
		return sv.listFor(peer), nil
	}
	if !serviceName.MatchString(a.Name) {
		return "", fmt.Errorf(`%s needs "name": small letters, digits and -, starting with a letter, at most 32`, ToolService)
	}
	switch a.Action {
	case "start":
		return sv.startFor(ctx, peer, g, s, a)
	case "status", "logs", "stop":
	default:
		return "", fmt.Errorf(`%s does "start", "status", "logs", "stop" or "list", not %q`, ToolService, a.Action)
	}
	sv.mu.Lock()
	sv.forget()
	svc := sv.find(peer, a.Name)
	sv.mu.Unlock()
	if svc == nil {
		return "", fmt.Errorf("there is no service %s: %s", a.Name, sv.listFor(peer))
	}
	switch a.Action {
	case "stop":
		if svc.running() {
			sv.stop(svc, "you stopped it")
		}
		return sv.status(svc, 20), nil
	case "logs":
		lines := a.Lines
		if lines <= 0 {
			lines = 40
		}
		return sv.status(svc, min(lines, 400)), nil
	}
	return sv.status(svc, 20), nil
}

// startFor starts the service a describes for peer.
func (sv *supervisor) startFor(ctx context.Context, peer string, g *guard.Guard, s settings, a serviceArgs) (string, error) {
	if strings.TrimSpace(a.Command) == "" {
		return "", fmt.Errorf(`%s needs "command" to start a service`, ToolService)
	}
	if a.Port < 0 || a.Port > 65535 {
		return "", fmt.Errorf("%d is no port", a.Port)
	}
	d, most, limit := ServiceTime, MaxServiceTime, MaxServices
	if peer != "" {
		d, most, limit = PeerServiceTime, MaxPeerServiceTime, MaxPeerServices
	}
	if a.Hours < 0 {
		return "", errors.New(`"hours" is how many hours it may run`)
	}
	if a.Hours > 0 {
		d = max(time.Minute, time.Duration(a.Hours*float64(time.Hour)))
	}
	if d > most {
		return "", fmt.Errorf("a service runs %s at most", hours(most))
	}
	dir, err := prepare(ctx, ToolService, g, a.Dir)
	if err != nil {
		return "", err
	}
	sv.mu.Lock()
	sv.forget()
	err = sv.admit(peer, a.Name, a.Port, limit)
	sv.mu.Unlock()
	if err != nil {
		return "", err
	}
	if a.Port > 0 && answers(a.Port) {
		return "", fmt.Errorf("something listens on port %d already: choose another port, or ask the user about what listens there", a.Port)
	}
	svc, err := sv.launch(a.Name, peer, a.Command, dir, a.Port, d, s)
	if err != nil {
		return "", err
	}
	ready := sv.waitReady(svc)
	if !svc.running() {
		return sv.status(svc, 40), nil
	}
	out := fmt.Sprintf("service %s runs since %s, until %s", svc.name, svc.p.start.Format("15:04"), svc.until.Format("15:04"))
	switch {
	case svc.port > 0 && ready:
		out += fmt.Sprintf(", and answers on localhost:%d", svc.port)
	case svc.port > 0:
		out += fmt.Sprintf(", but does not answer on localhost:%d after %s: look at it with \"status\"", svc.port, readyTime/time.Second*time.Second)
	}
	return out + ". It keeps running past this answer until it ends, it is stopped, its time is up, or the node stops or is rebuilt. " +
		"What it printed:\n" + svc.log.last(20), nil
}

// admit reports why the service name of peer cannot start, nil if it can.
// sv.mu is held.
func (sv *supervisor) admit(peer, name string, port, limit int) error {
	if sv.closed {
		return errors.New("the node stops: no service starts any more")
	}
	n := 0
	for _, s := range sv.all {
		if !s.running() {
			continue
		}
		if s.peer == peer && s.name == name {
			return fmt.Errorf("the service %s runs already: stop it, or choose another name", name)
		}
		if port > 0 && s.port == port {
			return fmt.Errorf("the service %s listens on port %d already", s.name, port)
		}
		if s.peer == peer {
			n++
		}
	}
	if n >= limit {
		return fmt.Errorf("%d services run already: stop one first", limit)
	}
	return nil
}

// waitReady waits for the port of the service to answer, as long as it
// runs, and reports whether it does. A service without a port is given a
// moment, for what it prints first.
func (sv *supervisor) waitReady(s *service) bool {
	if s.port == 0 {
		select {
		case <-s.p.done:
		case <-time.After(time.Second):
		}
		return false
	}
	deadline := time.Now().Add(readyTime)
	for time.Now().Before(deadline) {
		if answers(s.port) {
			return true
		}
		select {
		case <-s.p.done:
			return false
		case <-time.After(200 * time.Millisecond):
		}
	}
	return false
}

// answers reports whether something listens on port on localhost.
func answers(port int) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 200*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// status says how the service does, with its last lines of what it
// printed.
func (sv *supervisor) status(s *service, lines int) string {
	sv.mu.Lock()
	why, ended := s.why, s.ended
	sv.mu.Unlock()
	if s.running() {
		out := fmt.Sprintf("service %s runs, for %s, until %s", s.name, time.Since(s.p.start).Round(time.Second), s.until.Format("15:04"))
		if s.port > 0 {
			out += fmt.Sprintf(", on localhost:%d", s.port)
			if !answers(s.port) {
				out += ", which does not answer"
			}
		}
		return out + "; its last lines:\n" + s.log.last(lines)
	}
	took := s.p.took.Round(time.Second)
	var out string
	switch {
	case why != "":
		out = fmt.Sprintf("service %s was stopped after %s: %s", s.name, took, why)
	default:
		out = fmt.Sprintf("service %s ended by itself after %s: exit %d", s.name, took, s.p.cmd.ProcessState.ExitCode())
	}
	if !ended.IsZero() {
		out += ", at " + ended.Format("15:04")
	}
	return out + "; its last lines:\n" + s.log.last(lines)
}

// listFor lists the services of peer, "" for the owner.
func (sv *supervisor) listFor(peer string) string {
	sv.mu.Lock()
	defer sv.mu.Unlock()
	sv.forget()
	var lines []string
	for _, s := range sv.all {
		if s.peer != peer {
			continue
		}
		line := s.name + " · "
		if s.running() {
			line += fmt.Sprintf("runs for %s, until %s", time.Since(s.p.start).Round(time.Second), s.until.Format("15:04"))
		} else {
			line += "ended at " + s.ended.Format("15:04")
		}
		if s.port > 0 {
			line += fmt.Sprintf(" · localhost:%d", s.port)
		}
		lines = append(lines, line+" · "+firstLine(s.command))
	}
	if len(lines) == 0 {
		return "no service runs"
	}
	return strings.Join(lines, "\n")
}

// ShowService is what the owner is shown of a call of the tool for
// services.
func ShowService(raw json.RawMessage) string {
	var a serviceArgs
	if json.Unmarshal(raw, &a) != nil {
		return ""
	}
	switch a.Action {
	case "start":
		return "starting the service " + a.Name
	case "stop":
		return "stopping the service " + a.Name
	case "status", "logs":
		return "looking at the service " + a.Name
	case "list":
		return "listing the services"
	}
	return ""
}

// ring keeps the last serviceLog bytes a service printed.
type ring struct {
	mu  sync.Mutex
	buf []byte
}

func (r *ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if over := len(r.buf) - serviceLog; over > 0 {
		r.buf = r.buf[over:]
		// from the start of a line on
		if i := bytes.IndexByte(r.buf, '\n'); i >= 0 {
			r.buf = r.buf[i+1:]
		}
	}
	return len(p), nil
}

// last are the last n lines that were printed, "(nothing)" for none.
func (r *ring) last(n int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	text := strings.TrimRight(string(bytes.ToValidUTF8(r.buf, nil)), "\n")
	if text == "" {
		return "(nothing)"
	}
	lines := strings.Split(text, "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}
