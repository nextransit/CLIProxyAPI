package usage

import (
	"context"
	"testing"
	"time"
)

type blockingPlugin struct {
	started chan struct{}
	release chan struct{}
	done    chan struct{}
}

func (p *blockingPlugin) HandleUsage(ctx context.Context, record Record) {
	close(p.started)
	<-p.release
	close(p.done)
}

func TestManagerStop_WaitsForQueuedRecordsToDrain(t *testing.T) {
	t.Parallel()

	manager := NewManager(8)
	plugin := &blockingPlugin{
		started: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
	}
	manager.Register(plugin)
	manager.Publish(context.Background(), Record{Provider: "codex", Model: "gpt-5-codex"})

	select {
	case <-plugin.started:
	case <-time.After(2 * time.Second):
		t.Fatal("plugin did not start processing queued record")
	}

	stopReturned := make(chan struct{})
	go func() {
		manager.Stop()
		close(stopReturned)
	}()

	select {
	case <-stopReturned:
		t.Fatal("Stop() returned before queued record finished processing")
	case <-time.After(100 * time.Millisecond):
	}

	close(plugin.release)

	select {
	case <-plugin.done:
	case <-time.After(2 * time.Second):
		t.Fatal("plugin did not finish processing after release")
	}

	select {
	case <-stopReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return after queue drained")
	}
}
