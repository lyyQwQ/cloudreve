package workflows

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/enttest"
	enttask "github.com/cloudreve/Cloudreve/v4/ent/task"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/auth"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
	"github.com/cloudreve/Cloudreve/v4/pkg/cluster"
	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
	"github.com/cloudreve/Cloudreve/v4/pkg/downloader"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/manager"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
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

func TestRemoteDownloadTaskMonitorPollInterval(t *testing.T) {
	ctx := context.Background()
	node := &remoteDownloadFakeNode{settings: &types.NodeSetting{Interval: 10}}
	task := &RemoteDownloadTask{
		DBTask: &queue.DBTask{Task: &ent.Task{PublicState: &types.TaskPublicState{}}},
		node:   node,
		state:  &RemoteDownloadTaskState{Phase: RemoteDownloadTaskPhaseMonitor},
	}

	if got := task.monitorPollInterval(ctx); got != 10*time.Second {
		t.Fatalf("monitor interval = %s, want 10s", got)
	}

	task.state.Phase = RemoteDownloadTaskPhaseAwaitSeeding
	if got := task.monitorPollInterval(ctx); got != remoteDownloadAwaitSeedingMinPollInterval {
		t.Fatalf("seeding interval = %s, want %s", got, remoteDownloadAwaitSeedingMinPollInterval)
	}
}

func TestRemoteDownloadTaskTransferredOutputMatching(t *testing.T) {
	task := remoteDownloadSeedingTask(t, "cloudreve://my/downloads", []downloader.TaskFile{
		{Index: 1, Name: "dir/movie:name.mkv", Selected: true},
		{Index: 2, Name: "dir/other.mkv", Selected: true},
	}, map[int]interface{}{1: nil})

	exact, err := fs.NewUriFromString("cloudreve://my/downloads/dir/movie_name.mkv")
	if err != nil {
		t.Fatal(err)
	}
	ancestor, err := fs.NewUriFromString("cloudreve://my/downloads/dir")
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := fs.NewUriFromString("cloudreve://my/downloads/elsewhere")
	if err != nil {
		t.Fatal(err)
	}

	if ok, err := task.AffectedByDeletedURIs([]*fs.URI{exact}, nil, 0); err != nil || !ok {
		t.Fatalf("exact match = %v, %v; want true, nil", ok, err)
	}
	if ok, err := task.AffectedByDeletedURIs([]*fs.URI{ancestor}, nil, 0); err != nil || !ok {
		t.Fatalf("ancestor match = %v, %v; want true, nil", ok, err)
	}
	if ok, err := task.AffectedByDeletedURIs([]*fs.URI{unrelated}, nil, 0); err != nil || ok {
		t.Fatalf("unrelated match = %v, %v; want false, nil", ok, err)
	}
}

func TestRemoteDownloadTaskTransferredOutputMatchingFromSlaveState(t *testing.T) {
	uri, err := fs.NewUriFromString("cloudreve://my/slave/output.mkv")
	if err != nil {
		t.Fatal(err)
	}
	task := remoteDownloadSeedingTask(t, "cloudreve://my/downloads", nil, nil)
	task.state.SlaveUploadState = &SlaveUploadTaskState{
		Files: []SlaveUploadEntity{{Uri: uri, Index: 7}},
	}
	stateBytes, err := json.Marshal(task.state)
	if err != nil {
		t.Fatal(err)
	}
	task.Task.PrivateState = string(stateBytes)

	if ok, err := task.AffectedByDeletedURIs([]*fs.URI{uri}, nil, 0); err != nil || !ok {
		t.Fatalf("slave output match = %v, %v; want true, nil", ok, err)
	}
}

func TestRemoteDownloadTaskMonitorTaskMissingStatusSemantics(t *testing.T) {
	ctx := context.Background()
	node := &remoteDownloadFakeNode{settings: &types.NodeSetting{Interval: 10, WaitForSeeding: true}}
	for _, tc := range []struct {
		name  string
		phase RemoteDownloadTaskPhase
		want  enttask.Status
	}{
		{name: "before transfer", phase: RemoteDownloadTaskPhaseMonitor, want: enttask.StatusCanceled},
		{name: "after transfer", phase: RemoteDownloadTaskPhaseAwaitSeeding, want: enttask.StatusCompleted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := remoteDownloadSeedingTask(t, "cloudreve://my/downloads", []downloader.TaskFile{
				{Index: 1, Name: "movie.mkv", Selected: true},
			}, map[int]interface{}{1: nil})
			task.state.Phase = tc.phase
			task.node = node
			task.d = &remoteDownloadSpyDownloader{infoErr: downloader.ErrTaskNotFount}
			task.l = logging.NewConsoleLogger(logging.LevelError)

			got, err := task.monitor(ctx, nil)
			if err != nil {
				t.Fatalf("monitor: %v", err)
			}
			if got != tc.want {
				t.Fatalf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRemoteDownloadTaskCleanupOrphanSeedingOutputExistence(t *testing.T) {
	ctx := context.Background()
	dep, client, user := newRemoteDownloadWorkflowTestDep(t)
	defer client.Close()

	task := remoteDownloadSeedingTask(t, "cloudreve://my/downloads", []downloader.TaskFile{
		{Index: 1, Name: "missing.mkv", Selected: true},
	}, map[int]interface{}{1: nil})
	task.DBTask.DirectOwner = user
	spy := &remoteDownloadSpyDownloader{}
	task.d = spy

	cleaned, err := task.CleanupOrphanSeeding(ctx, dep)
	if err != nil {
		t.Fatalf("CleanupOrphanSeeding: %v", err)
	}
	if !cleaned {
		t.Fatal("CleanupOrphanSeeding should clean when all transferred outputs are missing")
	}
	if spy.cancelCalls != 1 {
		t.Fatalf("cancel calls = %d, want 1", spy.cancelCalls)
	}

	remaining, err := fs.NewUriFromString("cloudreve://my/downloads/remaining")
	if err != nil {
		t.Fatal(err)
	}
	fm := manager.NewFileManager(dep, user)
	if _, err := fm.Create(ctx, remaining, types.FileTypeFolder); err != nil {
		t.Fatalf("create remaining output: %v", err)
	}
	fm.Recycle()

	task = remoteDownloadSeedingTask(t, "cloudreve://my/downloads", []downloader.TaskFile{
		{Index: 1, Name: "missing.mkv", Selected: true},
		{Index: 2, Name: "remaining", Selected: true},
	}, map[int]interface{}{1: nil, 2: nil})
	task.DBTask.DirectOwner = user
	spy = &remoteDownloadSpyDownloader{}
	task.d = spy

	cleaned, err = task.CleanupOrphanSeeding(ctx, dep)
	if err != nil {
		t.Fatalf("CleanupOrphanSeeding with remaining output: %v", err)
	}
	if cleaned {
		t.Fatal("CleanupOrphanSeeding should keep task when one transferred output remains")
	}
	if spy.cancelCalls != 0 {
		t.Fatalf("cancel calls = %d, want 0", spy.cancelCalls)
	}
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

func newRemoteDownloadWorkflowTestDep(t *testing.T) (dependency.Dep, *ent.Client, *ent.User) {
	t.Helper()

	client := enttest.Open(t, "sqlite3", t.TempDir()+"/ent.db")
	group, err := client.Group.Create().
		SetName("g").
		SetPermissions(&boolset.BooleanSet{}).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create group: %v", err)
	}

	user, err := client.User.Create().
		SetEmail("u@example.com").
		SetNick("u").
		SetGroupUsers(group.ID).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	h, err := hashid.New("salt")
	if err != nil {
		t.Fatalf("hashid.New: %v", err)
	}

	cp := &remoteDownloadWorkflowTestConfigProvider{}
	cp.db.Type = conf.SQLiteDB
	cp.sys.Mode = conf.MasterMode

	dep := dependency.NewDependency(
		dependency.WithLogger(logging.NewConsoleLogger(logging.LevelError)),
		dependency.WithDbClient(client),
		dependency.WithConfigProvider(cp),
		dependency.WithHashIDEncoder(h),
		dependency.WithSettingProvider(setting.NewProvider(setting.NewDbDefaultStore(nil))),
	)

	return dep, client, user
}

type remoteDownloadWorkflowTestConfigProvider struct {
	db    conf.Database
	sys   conf.System
	ssl   conf.SSL
	unix  conf.Unix
	slave conf.Slave
	redis conf.Redis
	cors  conf.Cors
	over  map[string]any
}

func (c *remoteDownloadWorkflowTestConfigProvider) Database() *conf.Database { return &c.db }
func (c *remoteDownloadWorkflowTestConfigProvider) System() *conf.System     { return &c.sys }
func (c *remoteDownloadWorkflowTestConfigProvider) SSL() *conf.SSL           { return &c.ssl }
func (c *remoteDownloadWorkflowTestConfigProvider) Unix() *conf.Unix         { return &c.unix }
func (c *remoteDownloadWorkflowTestConfigProvider) Slave() *conf.Slave       { return &c.slave }
func (c *remoteDownloadWorkflowTestConfigProvider) Redis() *conf.Redis       { return &c.redis }
func (c *remoteDownloadWorkflowTestConfigProvider) Cors() *conf.Cors         { return &c.cors }
func (c *remoteDownloadWorkflowTestConfigProvider) OptionOverwrite() map[string]any {
	return c.over
}

func remoteDownloadSeedingTask(t *testing.T, dst string, files []downloader.TaskFile, transferred map[int]interface{}) *RemoteDownloadTask {
	t.Helper()

	state := &RemoteDownloadTaskState{
		Dst:         dst,
		Phase:       RemoteDownloadTaskPhaseAwaitSeeding,
		Handle:      &downloader.TaskHandle{ID: "id", Hash: "hash"},
		Status:      &downloader.TaskStatus{Name: "task", State: downloader.StatusSeeding, Files: files},
		Transferred: transferred,
	}
	stateBytes, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}

	return &RemoteDownloadTask{
		DBTask: &queue.DBTask{
			Task: &ent.Task{
				ID:           2001,
				Type:         queue.RemoteDownloadTaskType,
				Status:       enttask.StatusSuspending,
				PublicState:  &types.TaskPublicState{},
				PrivateState: string(stateBytes),
			},
		},
		state: state,
	}
}

type remoteDownloadSpyDownloader struct {
	cancelCalls    int
	setFilesCalls  int
	canceledHandle *downloader.TaskHandle
	infoStatus     *downloader.TaskStatus
	infoErr        error
	cancelErr      error
}

func (s *remoteDownloadSpyDownloader) CreateTask(context.Context, string, map[string]interface{}) (*downloader.TaskHandle, error) {
	return nil, nil
}

func (s *remoteDownloadSpyDownloader) Info(context.Context, *downloader.TaskHandle) (*downloader.TaskStatus, error) {
	if s.infoErr != nil {
		return nil, s.infoErr
	}
	return s.infoStatus, nil
}

func (s *remoteDownloadSpyDownloader) Cancel(_ context.Context, handle *downloader.TaskHandle) error {
	s.cancelCalls++
	s.canceledHandle = handle
	return s.cancelErr
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
	settings              *types.NodeSetting
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
	if n.settings != nil {
		return n.settings
	}
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
