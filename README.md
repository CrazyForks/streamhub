# streamhub

`streamhub` is a Redis-backed Go library for session-based streaming.

It is meant for the fairly common case where the code producing a stream and the code consuming it do not share the same lifetime, and may not even run on the same instance. `streamhub` keeps that state in Redis so publishing, subscribing, and canceling can be handled separately.

It works well for things like:

- LLM or SSE streaming
- incremental output from long-running tasks
- sharing the same session stream across multiple service instances
- reconnect, replay, and remote cancel support

## How it works

The library uses two Redis features:

- `Redis Streams` for storing chunks, so new subscribers can replay old data first
- `Redis Pub/Sub` for cancel signals, so cancels can be delivered quickly

Each producer registration also gets a `generation ID`. That is used as a fencing token so an old producer cannot keep writing into a newer stream after it has lost ownership.

## Requirements

- Go `1.26`
- Redis

## Install

```bash
go get github.com/gtoxlili/streamhub
```

## Minimal example

Create a `Hub` first:

```go
package main

import (
	"log"

	"github.com/gtoxlili/streamhub"
	"github.com/redis/rueidis"
)

func main() {
	client, err := rueidis.NewClient(rueidis.ClientOption{
		InitAddress: []string{"127.0.0.1:6379"},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	hub := streamhub.New(client)
	_ = hub
}
```

Register on the producer side:

```go
stream, created, err := hub.Register("chat:123", func() {
	// cancel your runtime task here
})
if err != nil {
	log.Fatal(err)
}
if !created {
	return
}
defer stream.Close()

stream.SetMetadata(map[string]any{
	"model": "demo-model",
})

stream.Publish("hello")
stream.Publish(" world")
```

The main thing to watch here is `created`. If it is `false`, that session already has an active stream, and this request should not start another producer.

Subscribers can attach like this:

```go
stream := hub.Get("chat:123")
if stream == nil {
	return
}

chunks, unsubscribe := stream.Subscribe(128)
defer unsubscribe()

for chunk := range chunks {
	// write to SSE / WebSocket / HTTP response
	println(chunk)
}
```

To cancel a session:

```go
stream := hub.Get("chat:123")
if stream != nil {
	stream.Cancel()
}
```

## API

### `streamhub.New(client)`

Creates a `Hub`.

### `hub.Register(sessionID, cancelRuntime)`

Tries to register a new stream.

- `sessionID` is your business/session identifier
- `cancelRuntime` is called when a cancel signal is received
- `created` tells you whether this call actually became the producer

### `hub.Get(sessionID)`

Returns a proxy for an existing stream. If Redis has no such stream, it returns `nil`.

### `hub.Active(sessionIDs)`

Checks which session IDs are still active.

### `hub.Remove(sessionID)`

Deletes the Redis keys and local state for a stream.

### `stream.Publish(chunk)`

Publishes a chunk.

### `stream.Subscribe(bufExtra)`

Subscribes to the stream. Existing chunks are replayed first, then new chunks are delivered.

### `stream.SetMetadata(v)` / `stream.Metadata(&target)`

Stores and loads per-stream metadata.

### `stream.Cancel()`

Sends a cancel signal.

### `stream.Close()`

Marks the stream as done and stops local subscriber loops.

### `stream.Done()`

Reports whether the stream is already done.

## Typical flow

A typical flow looks like this:

1. Call `Register` when a request comes in
2. Start the actual background job only when `created == true`
3. Keep calling `Publish` while the job is running
4. Let readers consume data through `Get(...).Subscribe(...)`
5. Call `Close` when the job finishes
6. Call `Cancel` if the user stops the session early

If your service runs on multiple instances, this is usually much easier to manage than keeping the stream state in process memory.

## Notes

- Do not start another producer when `created == false`
- Call `SetMetadata` before `Close`
- It is a good idea to always call the `unsubscribe` returned by `Subscribe`
- Stream lifetime and expiry are driven by Redis state

## License

GPL-3.0. See [LICENSE](./LICENSE).
