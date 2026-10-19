package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/a2a"
	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
)

// jobs are what a tool tells of the jobs of an answer: they run since at.
func jobs(at time.Time, commands ...string) []Job {
	var all []Job
	for i, c := range commands {
		all = append(all, Job{N: i + 1, Command: c, Since: at})
	}
	return all
}

// The jobs of an answer are told who watches its session as they change,
// and as none once it ends; what a tool tells after that is not. The owner
// can stop each while it runs.
func TestJobs(t *testing.T) {
	if TellJobs(context.Background(), nil, nil) {
		t.Fatal("outside an answer somebody is told")
	}
	at := time.Unix(100, 0)
	var stopped []int
	stop := func(n int) bool {
		stopped = append(stopped, n)
		return n == 1
	}
	late := make(chan context.Context, 2)
	gate := make(chan struct{})
	a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		if text == "none" {
			late <- ctx
			return "ran nothing", nil
		}
		TellJobs(ctx, jobs(at, "npm run dev"), stop)
		TellJobs(ctx, jobs(at, "npm run dev"), stop) // the same once more
		TellJobs(ctx, jobs(at, "npm run dev", "go test"), stop)
		late <- ctx
		<-gate
		return "ran them", nil
	}}
	s := service(t, a)
	ctx := context.Background()
	_, live, _ := s.Watch(ctx, "s1")
	s.Ask(ctx, "s1", "run")
	running := <-late
	list, err := s.Jobs(ctx)
	if err != nil || len(list) != 2 || list[0] != (Job{Session: "s1", Title: "run", N: 1, Command: "npm run dev", Since: at}) || list[1].N != 2 {
		t.Fatalf("jobs %+v, %v", list, err)
	}
	if err := s.StopJob(ctx, "s1", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.StopJob(ctx, "s1", 2); !errors.Is(err, ErrNoJob) || err.Error() != "no such job runs: job 2 in s1" {
		t.Fatalf("one that does not run: %v", err)
	}
	if err := s.StopJob(ctx, "s2", 1); !errors.Is(err, ErrNoJob) || len(stopped) != 2 {
		t.Fatalf("in another session: %v, %v", err, stopped)
	}
	close(gate)
	got := until(t, live, Answer)
	if kinds(got) != "message jobs jobs jobs answer" || got[1].Text != "job 1 · npm run dev" ||
		got[2].Text != "job 1 · npm run dev\njob 2 · go test" || got[3].Text != "" {
		t.Fatalf("events %+v", got)
	}
	TellJobs(running, jobs(at, "go test"), stop)
	if list, _ := s.Jobs(ctx); len(list) != 0 {
		t.Fatalf("after the answer: %+v", list)
	}

	// An answer without jobs is told none.
	s.Ask(ctx, "s1", "none")
	if got := until(t, live, Answer); kinds(got) != "message answer" {
		t.Fatalf("events %+v", got)
	}
	TellJobs(<-late, nil, nil)
	if past, _, _ := s.Watch(ctx, "s1"); kinds(past) != "message jobs jobs jobs answer message answer" {
		t.Fatalf("past %+v", past)
	}
}

// The jobs of an answer to a peer are among the jobs of the node, named by
// the conversation and the peer, until the answer ends; over the socket too.
func TestJobsOfPeers(t *testing.T) {
	at := time.Unix(100, 0)
	s, _, _ := archived(t, &fakeAgent{}, t.TempDir(), at)
	plain := service(t, &fakeAgent{}) // without an archive, which names the peer
	ctx := a2a.WithOrigin(context.Background(), a2a.Origin{PublicKey: "peer-key"})
	id := history.Party("peer-key") + "/" + history.Name("conv")
	stopped := 0
	for _, s := range []*Service{s, plain} {
		var later context.Context
		s.Peer(ctx, "conv", func(ctx context.Context) (string, error) {
			TellJobs(ctx, jobs(at, "serve", "watch"), func(n int) bool { stopped += n; return true })
			list, _ := s.Jobs(ctx)
			if len(list) != 2 || list[0].Session != id || list[0].Peer == "" || list[0].Title != "" {
				t.Errorf("jobs %+v", list)
			}
			c := NewClient(serve(t, Handler(s)).dir)
			if list, err := c.Jobs(ctx); err != nil || len(list) != 2 || list[1].Command != "watch" || !list[1].Since.Equal(at) {
				t.Errorf("over the socket: %+v, %v", list, err)
			}
			if err := c.StopJob(ctx, id, 2); err != nil {
				t.Errorf("stop over the socket: %v", err)
			}
			if err := c.StopJob(ctx, "s1", 1); !errors.Is(err, ErrNoJob) {
				t.Errorf("stop one that does not run, over the socket: %v", err)
			}
			// Once they ended, they are no more.
			TellJobs(ctx, nil, nil)
			if list, _ := s.Jobs(ctx); len(list) != 0 {
				t.Errorf("once they ended: %+v", list)
			}
			later = ctx
			return "", nil
		})
		TellJobs(later, jobs(at, "serve"), nil)
		if list, _ := s.Jobs(ctx); len(list) != 0 {
			t.Fatalf("after the answer: %+v", list)
		}
	}
	if stopped != 4 {
		t.Fatalf("stopped %d", stopped)
	}
}

// Jobs come the oldest first, then by session and number.
func TestJobsInOrder(t *testing.T) {
	s := service(t, &fakeAgent{})
	at := time.Unix(100, 0)
	s.setJobs("b", []Job{{N: 2, Since: at}, {N: 1, Since: at}}, nil)
	s.setJobs("a", []Job{{N: 3, Since: at.Add(time.Second)}, {N: 4, Since: at}}, nil)
	list, _ := s.Jobs(context.Background())
	var got []string
	for _, j := range list {
		got = append(got, j.Session+string(rune('0'+j.N)))
	}
	if want := "a4 b1 b2 a3"; strings.Join(got, " ") != want {
		t.Fatalf("order %s, want %s", got, want)
	}
	// The jobs of a session where nothing was said yet have no title.
	s.mu.Lock()
	s.get("a")
	s.mu.Unlock()
	list, _ = s.Jobs(context.Background())
	for _, j := range list {
		if j.Title != "" {
			t.Fatalf("job %+v", j)
		}
	}
}
