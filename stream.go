package streamhub

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gtoxlili/streamhub/pkg/json"
	"github.com/redis/rueidis"
)

const (
	// Keep this short so shutdown can be observed quickly.
	xreadBlock = 500 * time.Millisecond
	// Keep keys around a bit after Close so other subscribers can finish.
	keyTTL = 60 * time.Second
)

// LiveStream is a Redis-backed stream for one session.
// generation is only set on the producer side.
type LiveStream struct {
	client     rueidis.Client
	sessionID  string
	generation string // empty on subscriber-only proxies
	hub        *Hub

	mu     sync.Mutex
	subs   map[uint64]*subscriber
	nextID uint64

	closeOnce sync.Once
	closeCh   chan struct{} // closed by Close to stop local xreadLoops
}

type subscriber struct {
	ch     chan string
	cancel context.CancelFunc
}

func newLiveStream(hub *Hub, sessionID, generation string) *LiveStream {
	return &LiveStream{
		client:     hub.client,
		sessionID:  sessionID,
		generation: generation,
		hub:        hub,
		subs:       make(map[uint64]*subscriber),
		closeCh:    make(chan struct{}),
	}
}

// activeTTL is the lease for an active stream.
const activeTTL = 600 // seconds (10 min)

// Publish appends a chunk to Redis if the generation still matches.
func (s *LiveStream) Publish(chunk string) {
	ctx := context.Background()
	publishScript.Exec(ctx, s.client,
		[]string{metaKey(s.sessionID), chunksKey(s.sessionID)},
		[]string{s.generation, chunk, fmt.Sprint(activeTTL)},
	)
}

// SubscribeOption configures Subscribe behaviour.
type SubscribeOption func(*subscribeConfig)

type subscribeConfig struct {
	bufExtra    int
	batchReplay bool
}

// WithBatchReplay makes Subscribe concatenate all existing chunks into
// a single string instead of sending them one by one. Useful for
// reconnecting clients that don't need per-chunk granularity on replay.
func WithBatchReplay() SubscribeOption {
	return func(c *subscribeConfig) { c.batchReplay = true }
}

// WithBuffer sets the extra channel buffer size for live chunks.
// Defaults to 256 if not specified or ≤ 0.
func WithBuffer(n int) SubscribeOption {
	return func(c *subscribeConfig) { c.bufExtra = n }
}

// Subscribe replays existing chunks, then follows new ones.
// The returned unsubscribe should be called when the caller is done.
func (s *LiveStream) Subscribe(opts ...SubscribeOption) (<-chan string, func()) {
	cfg := subscribeConfig{bufExtra: 256}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.bufExtra <= 0 {
		cfg.bufExtra = 256
	}

	ctx := context.Background()
	key := chunksKey(s.sessionID)

	// Replay what is already in Redis.
	entries, _ := s.client.Do(ctx,
		s.client.B().Xrange().Key(key).Start("-").End("+").Build(),
	).AsXRange()

	lastID := "0-0"
	var ch chan string
	if len(entries) > 0 && cfg.batchReplay {
		ch = make(chan string, 1+cfg.bufExtra)
		var replay strings.Builder
		for _, e := range entries {
			if d, ok := e.FieldValues["d"]; ok {
				replay.WriteString(d)
			}
			lastID = e.ID
		}
		if replay.Len() > 0 {
			ch <- replay.String()
		}
	} else {
		ch = make(chan string, len(entries)+cfg.bufExtra)
		for _, e := range entries {
			if d, ok := e.FieldValues["d"]; ok {
				ch <- d
			}
			lastID = e.ID
		}
	}

	// The stream may have finished between XRANGE and Done.
	if s.Done() {
		s.drainRemaining(key, lastID, ch)
		close(ch)
		return ch, func() {}
	}

	// Switch to live delivery.
	subCtx, subCancel := context.WithCancel(context.Background())
	id := s.addSubscriber(ch, subCancel)
	go s.xreadLoop(subCtx, key, lastID, ch)

	return ch, func() {
		subCancel()
		s.removeSubscriber(id)
	}
}

func (s *LiveStream) addSubscriber(ch chan string, cancel context.CancelFunc) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextID
	s.nextID++
	s.subs[id] = &subscriber{ch: ch, cancel: cancel}
	return id
}

func (s *LiveStream) removeSubscriber(id uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subs, id)
}

// xreadLoop keeps reading new chunks until the stream ends or stops locally.
func (s *LiveStream) xreadLoop(ctx context.Context, key, cursor string, ch chan string) {
	defer close(ch)

	for {
		select {
		case <-ctx.Done():
			s.drainRemaining(key, cursor, ch)
			return
		case <-s.closeCh:
			s.drainRemaining(key, cursor, ch)
			return
		default:
		}

		var entries map[string][]rueidis.XRangeEntry
		err := s.client.Dedicated(func(dc rueidis.DedicatedClient) error {
			var readErr error
			entries, readErr = dc.Do(ctx,
				dc.B().Xread().Count(64).Block(xreadBlock.Milliseconds()).
					Streams().Key(key).Id(cursor).Build(),
			).AsXRead()
			return readErr
		})

		if err != nil {
			if ctx.Err() != nil {
				s.drainRemaining(key, cursor, ch)
				return
			}
			if rueidis.IsRedisNil(err) {
				// XREAD timed out, so check whether the stream ended.
				if s.Done() {
					s.drainRemaining(key, cursor, ch)
					return
				}
				continue
			}
			// Retry on transient errors.
			time.Sleep(100 * time.Millisecond)
			continue
		}

		for _, rangeEntries := range entries {
			for _, e := range rangeEntries {
				if d, ok := e.FieldValues["d"]; ok {
					ch <- d
				}
				cursor = e.ID
			}
		}
	}
}

// drainRemaining flushes chunks after cursor before closing ch.
// If the consumer is already gone, extra chunks are dropped.
func (s *LiveStream) drainRemaining(key, cursor string, ch chan string) {
	entries, err := s.client.Do(context.Background(),
		s.client.B().Xrange().Key(key).Start("("+cursor).End("+").Build(),
	).AsXRange()
	if err != nil {
		return
	}
	for _, e := range entries {
		if d, ok := e.FieldValues["d"]; ok {
			select {
			case ch <- d:
			default:
			}
		}
	}
}

// SetMetadata stores stream metadata as JSON.
// It only writes when the current generation still owns the stream.
func (s *LiveStream) SetMetadata(v any) {
	if s.generation == "" {
		return
	}
	ctx := context.Background()
	mk := metaKey(s.sessionID)
	// Do not let an old generation overwrite metadata.
	gen, _ := s.client.Do(ctx, s.client.B().Hget().Key(mk).Field("gen").Build()).ToString()
	if gen != s.generation {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.client.Do(ctx,
		s.client.B().Hset().Key(mk).FieldValue().FieldValue("metadata", string(b)).Build(),
	)
}

// Metadata loads stored metadata into target.
func (s *LiveStream) Metadata(target any) bool {
	raw, err := s.client.Do(context.Background(),
		s.client.B().Hget().Key(metaKey(s.sessionID)).Field("metadata").Build(),
	).ToString()
	if err != nil || raw == "" {
		return false
	}
	return json.UnmarshalString(raw, target) == nil
}

// Close marks the stream as done and stops local subscribers.
// If this generation is stale, Close only shuts down local state.
func (s *LiveStream) Close() {
	s.closeOnce.Do(func() {
		ctx := context.Background()
		mk := metaKey(s.sessionID)
		ck := chunksKey(s.sessionID)

		// Only the current owner can close the Redis-side stream.
		if s.generation != "" {
			gen, _ := s.client.Do(ctx,
				s.client.B().Hget().Key(mk).Field("gen").Build(),
			).ToString()
			if gen != s.generation {
				// A stale producer should only stop its own local work.
				close(s.closeCh)
				return
			}
		}

		// Mark the stream as done.
		s.client.Do(ctx, s.client.B().Hset().Key(mk).
			FieldValue().FieldValue("status", "done").Build())
		// Keep keys briefly so remote readers can observe the done state.
		s.client.Do(ctx, s.client.B().Expire().Key(mk).Seconds(int64(keyTTL.Seconds())).Build())
		s.client.Do(ctx, s.client.B().Expire().Key(ck).Seconds(int64(keyTTL.Seconds())).Build())

		// Stop local readers first, then close closeCh as a fallback.
		s.mu.Lock()
		for _, sub := range s.subs {
			sub.cancel()
		}
		s.mu.Unlock()
		close(s.closeCh)

		// Clean up local producer state.
		s.hub.cleanupLocal(s.sessionID, s.generation)
	})
}

// Cancel broadcasts a cancel signal and waits for the stream to finish
// (or the context to expire). Pass context.Background() for fire-and-forget.
func (s *LiveStream) Cancel(ctx context.Context) {
	mk := metaKey(s.sessionID)
	s.client.DoMulti(context.Background(),
		s.client.B().Hset().Key(mk).FieldValue().FieldValue("cancel", "1").Build(),
		s.client.B().Publish().Channel(cancelChannel(s.sessionID)).Message("cancel").Build(),
	)

	// Wait for the producer to finish (Close sets status=done).
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.Done() {
				return
			}
		}
	}
}

// Done reports whether the stream is marked done in Redis.
func (s *LiveStream) Done() bool {
	status, err := s.client.Do(context.Background(),
		s.client.B().Hget().Key(metaKey(s.sessionID)).Field("status").Build(),
	).ToString()
	return err == nil && status == "done"
}
