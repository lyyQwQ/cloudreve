package workflows

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/ent"
	enttask "github.com/cloudreve/Cloudreve/v4/ent/task"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/auth"
	"github.com/cloudreve/Cloudreve/v4/pkg/cluster"
	"github.com/cloudreve/Cloudreve/v4/pkg/downloader"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	"github.com/cloudreve/Cloudreve/v4/pkg/request"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
)

func TestRemoteDownloadTaskRestoredStateUserEntrypoints(t *testing.T) {
	ctx := context.Background()
	task := restoredRemoteDownloadTask(t)

	assertNotPanics(t, "Progress", func() {
		progress := task.Progress(ctx)
		if progress == nil {
			t.Fatal("Progress returned nil")
		}
	})

	assertNotPanics(t, "Summarize", func() {
		summary := task.Summarize(nil)
		if summary == nil {
			t.Fatal("Summarize returned nil")
		}
		if summary.Phase != string(RemoteDownloadTaskPhaseMonitor) {
			t.Fatalf("summary phase = %q, want %q", summary.Phase, RemoteDownloadTaskPhaseMonitor)
		}
	})

	assertNotPanics(t, "CancelDownload", func() {
		err := task.CancelDownload(ctx)
		if err == nil {
			t.Fatal("CancelDownload should return an error when downloader runtime cannot be recreated")
		}
		if !strings.Contains(err.Error(), "runtime is not initialized") {
			t.Fatalf("CancelDownload error = %q", err)
		}
	})
	if task.IsCanceled() {
		t.Fatal("CancelDownload should not mark a restored task canceled when downloader runtime is unavailable")
	}

	assertNotPanics(t, "SetDownloadTarget", func() {
		err := task.SetDownloadTarget(ctx, &downloader.SetFileToDownloadArgs{Index: 0, Download: true})
		if err == nil {
			t.Fatal("SetDownloadTarget should return an error when downloader runtime is unavailable")
		}
		if !strings.Contains(err.Error(), "runtime is not initialized") {
			t.Fatalf("SetDownloadTarget error = %q", err)
		}
	})

	assertNotPanics(t, "Cleanup", func() {
		if err := task.Cleanup(ctx); err != nil {
			t.Fatalf("Cleanup returned error: %v", err)
		}
	})

	assertRestoredHandle(t, task)
}

func TestRemoteDownloadTaskRestoredStateKeepsHandleAfterUserEntrypoints(t *testing.T) {
	task := restoredRemoteDownloadTask(t)

	if got := task.state; got != nil {
		t.Fatal("restored task should not eagerly initialize runtime state")
	}

	if summary := task.Summarize(nil); summary == nil {
		t.Fatal("Summarize returned nil")
	}

	assertRestoredHandle(t, task)
}

func TestRemoteDownloadTaskRestoredStateKeepsHandleForWorkerLoad(t *testing.T) {
	task := restoredRemoteDownloadTask(t)

	task.runtimeMu.Lock()
	defer task.runtimeMu.Unlock()
	if err := task.loadStateFromModelLocked(); err != nil {
		t.Fatalf("load state: %v", err)
	}
	if task.state == nil || task.state.Handle == nil {
		t.Fatal("worker state load dropped handle")
	}
	if task.state.Handle.ID != "restored-id" || task.state.Handle.Hash != "restored-hash" {
		t.Fatalf("worker handle = %#v", task.state.Handle)
	}

	assertRestoredHandle(t, task)
}

func TestRemoteDownloadTaskRestoredProgressDoesNotWaitForRuntimeLock(t *testing.T) {
	ctx := context.Background()
	task := restoredRemoteDownloadTask(t)

	task.runtimeMu.Lock()
	defer task.runtimeMu.Unlock()
	done := make(chan queue.Progresses, 1)
	go func() {
		done <- task.Progress(ctx)
	}()

	select {
	case progress := <-done:
		if progress == nil {
			t.Fatal("Progress returned nil")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Progress waited for runtime lock")
	}
}

func TestRemoteDownloadTaskRestoredStateConcurrentUserEntrypoints(t *testing.T) {
	ctx := context.Background()
	task := restoredRemoteDownloadTask(t)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(5)
		go func() {
			defer wg.Done()
			_ = task.Progress(ctx)
		}()
		go func() {
			defer wg.Done()
			_ = task.Summarize(nil)
		}()
		go func() {
			defer wg.Done()
			_ = task.CancelDownload(ctx)
		}()
		go func() {
			defer wg.Done()
			_ = task.SetDownloadTarget(ctx, &downloader.SetFileToDownloadArgs{Index: 0, Download: true})
		}()
		go func() {
			defer wg.Done()
			_ = task.Cleanup(ctx)
		}()
	}
	wg.Wait()

	if task.IsCanceled() {
		t.Fatal("concurrent restored entrypoints should not mark task canceled without downloader runtime")
	}
	assertRestoredHandle(t, task)
}

func TestRemoteDownloadTaskCancelRestoredTaskRecreatesDownloader(t *testing.T) {
	ctx := context.Background()
	task := restoredRemoteDownloadTask(t)
	spy := &remoteDownloadSpyDownloader{}
	node := &remoteDownloadFakeNode{downloader: spy}

	task.runtimeMu.Lock()
	task.node = node
	task.runtimeMu.Unlock()

	if err := task.CancelDownload(ctx); err != nil {
		t.Fatalf("CancelDownload: %v", err)
	}
	if node.createDownloaderCalls != 1 {
		t.Fatalf("CreateDownloader calls = %d, want 1", node.createDownloaderCalls)
	}
	if spy.cancelCalls != 1 {
		t.Fatalf("Cancel calls = %d, want 1", spy.cancelCalls)
	}
	if spy.canceledHandle == nil || spy.canceledHandle.Hash != "restored-hash" {
		t.Fatalf("canceled handle = %#v", spy.canceledHandle)
	}

	assertRestoredHandle(t, task)
}

func TestRemoteDownloadTaskSetDownloadTargetRestoredTaskRecreatesDownloader(t *testing.T) {
	ctx := context.Background()
	task := restoredRemoteDownloadTask(t)
	spy := &remoteDownloadSpyDownloader{}
	node := &remoteDownloadFakeNode{downloader: spy}

	task.runtimeMu.Lock()
	task.node = node
	task.runtimeMu.Unlock()

	if err := task.SetDownloadTarget(ctx, &downloader.SetFileToDownloadArgs{Index: 0, Download: true}); err != nil {
		t.Fatalf("SetDownloadTarget: %v", err)
	}
	if node.createDownloaderCalls != 1 {
		t.Fatalf("CreateDownloader calls = %d, want 1", node.createDownloaderCalls)
	}
	if spy.setFilesCalls != 1 {
		t.Fatalf("SetFilesToDownload calls = %d, want 1", spy.setFilesCalls)
	}

	assertRestoredHandle(t, task)
}

func restoredRemoteDownloadTask(t *testing.T) *RemoteDownloadTask {
	t.Helper()

	state := &RemoteDownloadTaskState{
		SrcUri: "magnet:?xt=urn:btih:test",
		Dst:    "cloudreve://my/files",
		Handle: &downloader.TaskHandle{
			ID:   "restored-id",
			Hash: "restored-hash",
		},
		Status: &downloader.TaskStatus{
			Name:     "restored task",
			State:    downloader.StatusDownloading,
			Total:    1024,
			SavePath: "/tmp/cloudreve-remote-download",
		},
		NodeState: NodeState{
			NodeID: 7,
		},
		Phase: RemoteDownloadTaskPhaseMonitor,
	}
	stateBytes, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}

	restored := NewRemoteDownloadTaskFromModel(&ent.Task{
		ID:           1001,
		Type:         queue.RemoteDownloadTaskType,
		Status:       enttask.StatusSuspending,
		PublicState:  &types.TaskPublicState{},
		PrivateState: string(stateBytes),
	})

	task, ok := restored.(*RemoteDownloadTask)
	if !ok {
		t.Fatalf("NewRemoteDownloadTaskFromModel returned %T", restored)
	}
	return task
}

type remoteDownloadSpyDownloader struct {
	cancelCalls    int
	setFilesCalls  int
	canceledHandle *downloader.TaskHandle
}

func (s *remoteDownloadSpyDownloader) CreateTask(context.Context, string, map[string]interface{}) (*downloader.TaskHandle, error) {
	return nil, nil
}

func (s *remoteDownloadSpyDownloader) Info(context.Context, *downloader.TaskHandle) (*downloader.TaskStatus, error) {
	return nil, nil
}

func (s *remoteDownloadSpyDownloader) Cancel(_ context.Context, handle *downloader.TaskHandle) error {
	s.cancelCalls++
	s.canceledHandle = handle
	return nil
}

func (s *remoteDownloadSpyDownloader) SetFilesToDownload(_ context.Context, _ *downloader.TaskHandle, _ ...*downloader.SetFileToDownloadArgs) error {
	s.setFilesCalls++
	return nil
}

func (s *remoteDownloadSpyDownloader) Test(context.Context) (string, error) {
	return "", nil
}

type remoteDownloadFakeNode struct {
	cluster.Node
	downloader            downloader.Downloader
	createDownloaderCalls int
}

func (n *remoteDownloadFakeNode) CreateDownloader(context.Context, request.Client, setting.Provider) (downloader.Downloader, error) {
	n.createDownloaderCalls++
	return n.downloader, nil
}

func (n *remoteDownloadFakeNode) ID() int {
	return 7
}

func (n *remoteDownloadFakeNode) Name() string {
	return "fake"
}

func (n *remoteDownloadFakeNode) IsMaster() bool {
	return true
}

func (n *remoteDownloadFakeNode) CreateTask(context.Context, string, string) (int, error) {
	return 0, nil
}

func (n *remoteDownloadFakeNode) GetTask(context.Context, int, bool) (*cluster.SlaveTaskSummary, error) {
	return nil, nil
}

func (n *remoteDownloadFakeNode) CleanupFolders(context.Context, ...string) error {
	return nil
}

func (n *remoteDownloadFakeNode) AuthInstance() auth.Auth {
	return nil
}

func (n *remoteDownloadFakeNode) Settings(context.Context) *types.NodeSetting {
	return &types.NodeSetting{}
}

func (n *remoteDownloadFakeNode) PrepareUpload(context.Context, *fs.StatelessPrepareUploadService) (*fs.StatelessPrepareUploadResponse, error) {
	return nil, nil
}

func (n *remoteDownloadFakeNode) CompleteUpload(context.Context, *fs.StatelessCompleteUploadService) error {
	return nil
}

func (n *remoteDownloadFakeNode) OnUploadFailed(context.Context, *fs.StatelessOnUploadFailedService) error {
	return nil
}

func (n *remoteDownloadFakeNode) CreateFile(context.Context, *fs.StatelessCreateFileService) error {
	return nil
}

func assertRestoredHandle(t *testing.T, task *RemoteDownloadTask) {
	t.Helper()

	var persisted RemoteDownloadTaskState
	if err := json.Unmarshal([]byte(task.State()), &persisted); err != nil {
		t.Fatalf("unmarshal persisted state: %v", err)
	}
	if persisted.Handle == nil {
		t.Fatal("persisted handle was dropped")
	}
	if persisted.Handle.ID != "restored-id" || persisted.Handle.Hash != "restored-hash" {
		t.Fatalf("persisted handle = %#v", persisted.Handle)
	}
}

func assertNotPanics(t *testing.T, name string, fn func()) {
	t.Helper()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("%s panicked: %v", name, r)
		}
	}()
	fn()
}
