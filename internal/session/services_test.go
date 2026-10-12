package session

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The services the tools keep running are listed the oldest first, those of
// peers with their names, also over the socket, and stopped by their IDs.
func TestServices(t *testing.T) {
	s := service(t, &fakeAgent{})
	ctx := context.Background()
	if list, err := s.Services(ctx); err != nil || len(list) != 0 {
		t.Fatalf("without a source: %v, %v", list, err)
	}
	at := time.Unix(100, 0)
	var stopped []string
	s.ServicesFrom(func() []Services {
		return []Services{
			{List: func() []ServiceInfo { return []ServiceInfo{{ID: "b", Name: "web", Since: at.Add(time.Second)}} }, Stop: func(id string) bool { return false }},
			{List: func() []ServiceInfo { return []ServiceInfo{{ID: "a", Name: "mock", PeerKey: "peer-key", Since: at}} },
				Stop: func(id string) bool { stopped = append(stopped, id); return id == "a" }},
		}
	})
	for _, api := range []API{s, NewClient(serve(t, Handler(s)).dir)} {
		list, err := api.Services(ctx)
		if err != nil || len(list) != 2 || list[0].ID != "a" || list[0].Peer == "" || list[1].Name != "web" || list[1].Peer != "" {
			t.Fatalf("services %+v, %v", list, err)
		}
		if err := api.StopService(ctx, "a"); err != nil {
			t.Errorf("stop: %v", err)
		}
		if err := api.StopService(ctx, "c"); !errors.Is(err, ErrNoService) {
			t.Errorf("stop one that does not run: %v", err)
		}
	}
	if len(stopped) != 4 {
		t.Errorf("stopped %v", stopped)
	}
}
