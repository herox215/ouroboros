package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"time"

	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/internal/node"
)

// The front ends reach the Service of the node over its socket (see
// home.Dir.Socket) with HTTP: a request of JSON for each method of API, and
// for Watch a response that goes on, one Event of JSON a line, after a first
// line with what happened before. Nothing of it leaves the host: the socket
// is a file only the user who runs the node can open.

// maxRequest is how long the body of a request may be: a message of the owner
// with a file pasted into it fits.
const maxRequest = 16 << 20

// codes name the errors a client tells apart, on the socket.
var codes = map[string]error{
	"busy":       ErrBusy,
	"restarting": ErrRestarting,
	"stopped":    ErrStopped,
	"no_session": ErrNoSession,
	"empty":      ErrEmpty,
	"no_effort":  ErrNoEffort,
	"no_job":     ErrNoJob,
	"no_service": ErrNoService,
	"not_set_up": config.ErrIncomplete,
}

// failure is an error on the socket.
type failure struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

type (
	askRequest struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	idRequest struct {
		ID string `json:"id"`
	}
	jobRequest struct {
		ID string `json:"id"`
		N  int    `json:"n"`
	}
	serviceRequest struct {
		ID string `json:"id"`
	}
	effortRequest struct {
		ID     string `json:"id"`
		Effort string `json:"effort"`
	}
	unsteerResponse struct {
		Text string `json:"text"`
	}
	newResponse struct {
		ID string `json:"id"`
	}
	contentsResponse struct {
		Lines []Line `json:"lines"`
		Error string `json:"error,omitempty"`
	}
	releaseResponse struct {
		Stopping bool `json:"stopping"`
	}
	// pastLine is the first line of the response to a watch.
	pastLine struct {
		Past []Event `json:"past"`
	}
)

// Handler serves s on the socket of the node.
func Handler(s *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /new", func(w http.ResponseWriter, r *http.Request) {
		id, _ := s.New(r.Context()) // a Service always has one
		reply(w, newResponse{id}, nil)
	})
	mux.HandleFunc("POST /ask", func(w http.ResponseWriter, r *http.Request) {
		var in askRequest
		if read(w, r, &in) {
			reply(w, nil, s.Ask(r.Context(), in.ID, in.Text))
		}
	})
	mux.HandleFunc("POST /steer", func(w http.ResponseWriter, r *http.Request) {
		var in askRequest
		if read(w, r, &in) {
			reply(w, nil, s.Steer(r.Context(), in.ID, in.Text))
		}
	})
	mux.HandleFunc("POST /unsteer", func(w http.ResponseWriter, r *http.Request) {
		var in idRequest
		if read(w, r, &in) {
			text, err := s.Unsteer(r.Context(), in.ID)
			reply(w, unsteerResponse{text}, err)
		}
	})
	mux.HandleFunc("POST /cancel", func(w http.ResponseWriter, r *http.Request) {
		var in idRequest
		if read(w, r, &in) {
			reply(w, nil, s.Cancel(r.Context(), in.ID))
		}
	})
	mux.HandleFunc("POST /effort", func(w http.ResponseWriter, r *http.Request) {
		var in effortRequest
		if read(w, r, &in) {
			reply(w, nil, s.SetEffort(r.Context(), in.ID, in.Effort))
		}
	})
	mux.HandleFunc("POST /delete", func(w http.ResponseWriter, r *http.Request) {
		var in idRequest
		if read(w, r, &in) {
			reply(w, nil, s.Delete(r.Context(), in.ID))
		}
	})
	mux.HandleFunc("GET /list", func(w http.ResponseWriter, r *http.Request) {
		list, _ := s.List(r.Context()) // a Service always has its list
		reply(w, list, nil)
	})
	mux.HandleFunc("GET /peers", func(w http.ResponseWriter, r *http.Request) {
		list, err := s.Peers(r.Context())
		reply(w, list, err)
	})
	mux.HandleFunc("GET /jobs", func(w http.ResponseWriter, r *http.Request) {
		jobs, _ := s.Jobs(r.Context()) // a Service always knows its jobs
		reply(w, jobs, nil)
	})
	mux.HandleFunc("POST /stopjob", func(w http.ResponseWriter, r *http.Request) {
		var in jobRequest
		if read(w, r, &in) {
			reply(w, nil, s.StopJob(r.Context(), in.ID, in.N))
		}
	})
	mux.HandleFunc("GET /services", func(w http.ResponseWriter, r *http.Request) {
		services, _ := s.Services(r.Context()) // a Service always knows its services
		reply(w, services, nil)
	})
	mux.HandleFunc("POST /stopservice", func(w http.ResponseWriter, r *http.Request) {
		var in serviceRequest
		if read(w, r, &in) {
			reply(w, nil, s.StopService(r.Context(), in.ID))
		}
	})
	mux.HandleFunc("GET /read", func(w http.ResponseWriter, r *http.Request) {
		events, err := s.Read(r.Context(), r.URL.Query().Get("id"))
		reply(w, events, err)
	})
	mux.HandleFunc("GET /contents", func(w http.ResponseWriter, r *http.Request) {
		lines, err := s.Contents(r.Context(), r.URL.Query().Get("id"))
		if errors.Is(err, ErrNoSession) {
			reply(w, nil, err)
			return
		}
		out := contentsResponse{Lines: lines}
		if err != nil {
			out.Error = err.Error()
		}
		reply(w, out, nil)
	})
	mux.HandleFunc("POST /release", func(w http.ResponseWriter, r *http.Request) {
		reply(w, releaseResponse{s.Releases()}, nil)
	})
	mux.HandleFunc("POST /keep", func(w http.ResponseWriter, r *http.Request) {
		s.Keep()
		reply(w, struct{}{}, nil)
	})
	mux.HandleFunc("GET /watch", func(w http.ResponseWriter, r *http.Request) {
		past, live, err := s.Watch(r.Context(), r.URL.Query().Get("id"))
		if err != nil {
			reply(w, nil, err)
			return
		}
		// What cannot be written is of a client that is gone: then the
		// context of the request ends, and with it live.
		w.Header().Set("Content-Type", "application/x-ndjson")
		flush := http.NewResponseController(w).Flush
		enc := json.NewEncoder(w)
		enc.Encode(pastLine{past})
		flush()
		for e := range live {
			enc.Encode(e)
			flush()
		}
	})
	return mux
}

// read decodes the body of r into in. A body that cannot be read is answered
// as a bad request, and read reports false.
func read(w http.ResponseWriter, r *http.Request, in any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequest)).Decode(in); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(failure{Error: "the request cannot be read: " + err.Error()})
		return false
	}
	return true
}

// reply answers with out as JSON, or with err unless it is nil.
func reply(w http.ResponseWriter, out any, err error) {
	if err != nil {
		f := failure{Error: err.Error()}
		for code, known := range codes {
			if errors.Is(err, known) {
				f.Code = code
			}
		}
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(f)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// ErrNotRunning means that the node that holds the sessions does not run.
var ErrNotRunning = errors.New("the node is not running")

// Client is the API of the sessions of the node that runs on this host, over
// its socket.
type Client struct {
	local   node.Local
	http    *http.Client
	profile string // the ID of the profile of the sessions, "" for the first
	// Patience is how long the node may take to stop once it was released.
	Patience time.Duration
	// Listening is how long a node that runs may take to listen on its
	// socket: it opens it right after it took its PID file.
	Listening time.Duration
}

// NewClient returns the client of the sessions of the node in dir, or of
// the profile of a node in dir, see home.Dir.Profiles.
func NewClient(dir home.Dir) *Client {
	c := &Client{local: node.Local{Home: dir}, Patience: time.Minute, Listening: 5 * time.Second}
	if dir.Root() != dir {
		c.profile = filepath.Base(string(dir)) // named after its ID
	}
	c.http = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return c.dial(ctx, dir.Socket())
	}}}
	return c
}

// pollInterval is how often the client looks whether the node is up or gone.
const pollInterval = 50 * time.Millisecond

// dial connects to the socket at path. A node that runs and does not listen
// yet is waited for a while: it opens its socket right after it took its PID
// file.
func (c *Client) dial(ctx context.Context, path string) (net.Conn, error) {
	deadline := time.Now().Add(c.Listening)
	for {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "unix", path)
		if err == nil {
			return conn, nil
		}
		if _, running := c.local.Running(); !running {
			return nil, ErrNotRunning
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil, fmt.Errorf("the node does not answer on %s, is it of an older build? (ouro stop, then ouro start): %w", path, err)
		}
		time.Sleep(pollInterval)
	}
}

// do sends a request with the body in, as JSON, and decodes the answer into
// out unless it is nil. An error the Service gave comes back as it was, as
// far as codes knows it.
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	resp, err := c.request(ctx, method, path, in)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// request sends a request and returns its response if it succeeded.
func (c *Client) request(ctx context.Context, method, path string, in any) (*http.Response, error) {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in) // the requests are plain data
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(ctx, method, "http://ouro"+path, body) // the method and the path are the client's
	if c.profile != "" {
		req.Header.Set(node.ProfileHeader, c.profile)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, ErrNotRunning) {
			return nil, ErrNotRunning
		}
		return nil, err
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil
	}
	defer resp.Body.Close()
	var f failure
	if err := json.NewDecoder(resp.Body).Decode(&f); err != nil || f.Error == "" {
		return nil, fmt.Errorf("the node answered %s", resp.Status)
	}
	if known, ok := codes[f.Code]; ok {
		return nil, known
	}
	return nil, errors.New(f.Error)
}

// New returns the ID of a new session.
func (c *Client) New(ctx context.Context) (string, error) {
	var out newResponse
	err := c.do(ctx, http.MethodPost, "/new", struct{}{}, &out)
	return out.ID, err
}

// Ask starts the answer to text in the session id.
func (c *Client) Ask(ctx context.Context, id, text string) error {
	return c.do(ctx, http.MethodPost, "/ask", askRequest{id, text}, nil)
}

// Steer gives text to the answer on its way in the session id.
func (c *Client) Steer(ctx context.Context, id, text string) error {
	return c.do(ctx, http.MethodPost, "/steer", askRequest{id, text}, nil)
}

// Unsteer takes back the last message that is queued in the session id.
func (c *Client) Unsteer(ctx context.Context, id string) (string, error) {
	var out unsteerResponse
	err := c.do(ctx, http.MethodPost, "/unsteer", idRequest{id}, &out)
	return out.Text, err
}

// Cancel stops the answer on its way in the session id.
func (c *Client) Cancel(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/cancel", idRequest{id}, nil)
}

// SetEffort makes effort how much the model thinks in the session id.
func (c *Client) SetEffort(ctx context.Context, id, effort string) error {
	return c.do(ctx, http.MethodPost, "/effort", effortRequest{id, effort}, nil)
}

// Delete removes the session id.
func (c *Client) Delete(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/delete", idRequest{id}, nil)
}

// List describes the sessions that have a message, the latest first.
func (c *Client) List(ctx context.Context) ([]Info, error) {
	var list []Info
	err := c.do(ctx, http.MethodGet, "/list", nil, &list)
	return list, err
}

// Peers describes the conversations of peers, the latest first.
func (c *Client) Peers(ctx context.Context) ([]Info, error) {
	var list []Info
	err := c.do(ctx, http.MethodGet, "/peers", nil, &list)
	return list, err
}

// Jobs are the commands that run in the background for the answers on
// their way, the oldest first.
func (c *Client) Jobs(ctx context.Context) ([]Job, error) {
	var jobs []Job
	err := c.do(ctx, http.MethodGet, "/jobs", nil, &jobs)
	return jobs, err
}

// StopJob stops the job n of the answer on its way in the session or
// conversation of a peer id.
func (c *Client) StopJob(ctx context.Context, id string, n int) error {
	return c.do(ctx, http.MethodPost, "/stopjob", jobRequest{id, n}, nil)
}

// Services are the services that run past the answers, the oldest first.
func (c *Client) Services(ctx context.Context) ([]ServiceInfo, error) {
	var services []ServiceInfo
	err := c.do(ctx, http.MethodGet, "/services", nil, &services)
	return services, err
}

// StopService stops the service id.
func (c *Client) StopService(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/stopservice", serviceRequest{id}, nil)
}

// Read returns what happened in the conversation of a peer id.
func (c *Client) Read(ctx context.Context, id string) ([]Event, error) {
	var events []Event
	err := c.do(ctx, http.MethodGet, "/read?id="+url.QueryEscape(id), nil, &events)
	return events, err
}

// Contents returns what the next call of the LLM in the session id is sent.
func (c *Client) Contents(ctx context.Context, id string) ([]Line, error) {
	var out contentsResponse
	if err := c.do(ctx, http.MethodGet, "/contents?id="+url.QueryEscape(id), nil, &out); err != nil {
		return nil, err
	}
	if out.Error != "" {
		return out.Lines, errors.New(out.Error)
	}
	return out.Lines, nil
}

// Watch returns what happened in the session id and what happens from now
// on, until ctx ends or the node stops.
func (c *Client) Watch(ctx context.Context, id string) ([]Event, <-chan Event, error) {
	resp, err := c.request(ctx, http.MethodGet, "/watch?id="+url.QueryEscape(id), nil)
	if err != nil {
		return nil, nil, err
	}
	dec := json.NewDecoder(resp.Body)
	var first pastLine
	if err := dec.Decode(&first); err != nil {
		resp.Body.Close()
		return nil, nil, fmt.Errorf("the node did not say what happened: %w", err)
	}
	live := make(chan Event)
	go func() {
		defer close(live)
		defer resp.Body.Close()
		for {
			var e Event
			if dec.Decode(&e) != nil {
				return
			}
			select {
			case live <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	return first.Past, live, nil
}

// Open starts the node unless it runs, and reports whether it started it. A
// node that is not set up fails with config.ErrIncomplete.
func (c *Client) Open(context.Context) (bool, error) {
	if _, running := c.local.Running(); running {
		return false, nil
	}
	_, err := c.local.Start()
	return err == nil, err
}

// Keep makes the node that runs run on, also when the front end that
// started it lets go of it: see Service.Keep.
func (c *Client) Keep(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/keep", struct{}{}, nil)
}

// Release lets the node stop once no answer is on its way, and waits until it
// stopped unless an answer keeps it running. A node that does not run is
// stopped already.
func (c *Client) Release(ctx context.Context) error {
	var out releaseResponse
	err := c.do(ctx, http.MethodPost, "/release", struct{}{}, &out)
	if errors.Is(err, ErrNotRunning) {
		return nil
	}
	// A node that stops on the way, as by ouro stop, is let go of: it
	// gives up its PID file a moment after its socket.
	if err != nil {
		for deadline := time.Now().Add(time.Second); ; time.Sleep(pollInterval) {
			if _, running := c.local.Running(); !running {
				return nil
			}
			if time.Now().After(deadline) {
				return err
			}
		}
	}
	if err != nil || !out.Stopping {
		return err
	}
	for deadline := time.Now().Add(c.Patience); time.Now().Before(deadline); time.Sleep(pollInterval) {
		if _, running := c.local.Running(); !running {
			return nil
		}
	}
	return errors.New("the node did not stop in time")
}
