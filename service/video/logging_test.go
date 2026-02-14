package video

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
)

func TestVideoTaskLogsStartWithTypeAndFileID(t *testing.T) {
	l := &memLogger{}
	ctx := context.WithValue(context.Background(), logging.LoggerCtx{}, l)

	tk, err := queue.NewVideoSubtitleBurnTask(ctx, 42, nil, nil)
	if err != nil {
		t.Fatalf("NewVideoSubtitleBurnTask: %v", err)
	}

	_, err = tk.Do(ctx)
	if err == nil {
		t.Fatalf("expected Do error when dependency is missing")
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	joined := ""
	if l.infos != nil {
		joined = strings.Join(*l.infos, "\n")
	}
	if !strings.Contains(joined, "task_type=") || !strings.Contains(joined, "file_id=42") {
		t.Fatalf("expected start log with task_type and file_id, got: %s", joined)
	}
}
