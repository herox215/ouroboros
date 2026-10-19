package matrix

import (
	"context"
	"errors"
	"sync"
	"time"

	"ouroboros/plugin"
)

// connection keeps the Matrix SDK behind a small boundary for lifecycle tests.
// Its implementation must recheck room membership before sending any text.
type connection interface {
	Run(context.Context, func(context.Context, incoming) error) error
	SendTo(context.Context, string, string, string) error
	TypingTo(context.Context, string, string, bool) error
	Close() error
}
type incoming struct {
	RoomID, Sender, Text string
	Images               []plugin.Image
}

type controller struct {
	store   store
	connect func(context.Context, configuration) (connection, error)
	receive func(context.Context, connection, incoming) error
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
}

func (c *controller) start() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.done = make(chan struct{})
	go func() { defer close(c.done); c.run(ctx) }()
}
func (c *controller) stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel == nil {
		return
	}
	c.cancel()
	<-c.done
	c.cancel = nil
}

func (c *controller) run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var revision string
	var active bool
	var cancel context.CancelFunc
	var done chan struct{}
	var statusMu sync.Mutex
	status := "Connecting"
	var observed func() string
	stop := func() {
		if cancel != nil {
			cancel()
			<-done
			cancel = nil
		}
	}
	defer stop()
	for {
		cfg, err := c.store.load()
		if err != nil {
			stop()
			active = false
			revision = ""
		} else {
			if !active || cfg.Revision != revision {
				stop()
				revision = cfg.Revision
				active = true
				if cfg.Enabled && cfg.Token != "" {
					runCtx, halt := context.WithCancel(ctx)
					cancel = halt
					done = make(chan struct{})
					statusMu.Lock()
					status = "Connecting"
					statusMu.Unlock()
					go func(cfg configuration, finished chan struct{}) {
						defer close(finished)
						for runCtx.Err() == nil {
							conn, err := c.connect(runCtx, cfg)
							if err == nil {
								statusMu.Lock()
								status = "Synchronizing"
								if health, ok := conn.(interface{ Status() string }); ok {
									observed = health.Status
								}
								statusMu.Unlock()
								guarded := guardedConnection{connection: conn, store: c.store, revision: cfg.Revision}
								_ = conn.Run(runCtx, func(msgCtx context.Context, msg incoming) error {
									// A settings change revokes access immediately, even during the
									// one-second configuration reload interval.
									current, readErr := c.store.load()
									if readErr != nil || !current.Enabled || current.Revision != cfg.Revision {
										return errors.New("gateway configuration changed")
									}
									statusMu.Lock()
									status = "Connected"
									statusMu.Unlock()
									err := c.receive(msgCtx, guarded, msg)
									if err != nil && !errors.Is(err, plugin.ErrGatewayDenied) {
										_ = guarded.SendTo(msgCtx, msg.RoomID, msg.Sender, "Ouro could not accept this message. Check the local session and gateway settings.")
									}
									return err
								})
								_ = conn.Close()
							}
							if runCtx.Err() != nil {
								return
							}
							statusMu.Lock()
							observed = nil
							status = "Connection failed; retrying"
							statusMu.Unlock()
							// Do not write SDK errors: a homeserver can echo request secrets.
							select {
							case <-runCtx.Done():
								return
							case <-time.After(5 * time.Second):
							}
						}
					}(cfg, done)
				}
			}
			text := "Disabled"
			if cfg.Enabled {
				text = "Not logged in"
				if cfg.Token != "" {
					statusMu.Lock()
					text = status
					readStatus := observed
					statusMu.Unlock()
					if readStatus != nil {
						text = readStatus()
					}
				}
			}
			if cfg.Enabled {
				c.store.status(cfg.Revision, text)
			}
		}
		select {
		case <-ctx.Done():
			c.store.status(revision, "Stopped")
			return
		case <-ticker.C:
		}
	}
}
