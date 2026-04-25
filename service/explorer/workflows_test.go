package explorer

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/enttest"
	enttask "github.com/cloudreve/Cloudreve/v4/ent/task"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/workflows"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	"github.com/cloudreve/Cloudreve/v4/pkg/setting"
	"github.com/gin-gonic/gin"
	"github.com/gofrs/uuid"
)

type explorerTestConfigProvider struct {
	db    conf.Database
	sys   conf.System
	ssl   conf.SSL
	unix  conf.Unix
	slave conf.Slave
	redis conf.Redis
	cors  conf.Cors
	over  map[string]any
}

func (c *explorerTestConfigProvider) Database() *conf.Database        { return &c.db }
func (c *explorerTestConfigProvider) System() *conf.System            { return &c.sys }
func (c *explorerTestConfigProvider) SSL() *conf.SSL                  { return &c.ssl }
func (c *explorerTestConfigProvider) Unix() *conf.Unix                { return &c.unix }
func (c *explorerTestConfigProvider) Slave() *conf.Slave              { return &c.slave }
func (c *explorerTestConfigProvider) Redis() *conf.Redis              { return &c.redis }
func (c *explorerTestConfigProvider) Cors() *conf.Cors                { return &c.cors }
func (c *explorerTestConfigProvider) OptionOverwrite() map[string]any { return c.over }

func newExplorerTestDep(t *testing.T) (dependency.Dep, *ent.Client, *ent.User) {
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

	cp := &explorerTestConfigProvider{}
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

func createTaskForUser(t *testing.T, client *ent.Client, ownerID int, taskType string) {
	t.Helper()

	if _, err := client.Task.Create().
		SetType(taskType).
		SetUserID(ownerID).
		SetCorrelationID(uuid.Must(uuid.NewV4())).
		SetPublicState(&types.TaskPublicState{}).
		Save(context.Background()); err != nil {
		t.Fatalf("create task %s: %v", taskType, err)
	}
}

func newExplorerContext(dep dependency.Dep, user *ent.User) *gin.Context {
	w := httptest.NewRecorder()
	c, e := gin.CreateTestContext(w)
	e.ContextWithFallback = true
	req := httptest.NewRequest("GET", "/api/v4/tasks", nil)
	ctx := context.WithValue(req.Context(), dependency.DepCtx{}, dep)
	ctx = context.WithValue(ctx, inventory.UserCtx{}, user)
	c.Request = req.WithContext(ctx)
	return c
}

func TestListTasksGeneralIncludesVideoWorkflowTypes(t *testing.T) {
	dep, client, user := newExplorerTestDep(t)
	defer client.Close()

	otherUser, err := client.User.Create().
		SetEmail("other@example.com").
		SetNick("other").
		SetGroupUsers(user.GroupUsers).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create other user: %v", err)
	}

	createTaskForUser(t, client, user.ID, queue.VideoHLSSliceTaskType)
	createTaskForUser(t, client, user.ID, queue.VideoSubtitleBurnTaskType)
	createTaskForUser(t, client, user.ID, queue.RemoteDownloadTaskType)
	createTaskForUser(t, client, otherUser.ID, queue.VideoHLSSliceTaskType)

	service := &ListTaskService{PageSize: 20, Category: "general"}
	resp, err := service.ListTasks(newExplorerContext(dep, user))
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}

	typesInResp := make(map[string]struct{}, len(resp.Tasks))
	for _, item := range resp.Tasks {
		typesInResp[item.Type] = struct{}{}
	}

	if _, ok := typesInResp[queue.VideoHLSSliceTaskType]; !ok {
		t.Fatalf("expected %s in response, got %+v", queue.VideoHLSSliceTaskType, resp.Tasks)
	}
	if _, ok := typesInResp[queue.VideoSubtitleBurnTaskType]; !ok {
		t.Fatalf("expected %s in response, got %+v", queue.VideoSubtitleBurnTaskType, resp.Tasks)
	}
	if _, ok := typesInResp[queue.RemoteDownloadTaskType]; ok {
		t.Fatalf("did not expect %s in general task list, got %+v", queue.RemoteDownloadTaskType, resp.Tasks)
	}
}

func TestCancelDownloadTaskPersistsCanceledStatus(t *testing.T) {
	dep, client, user := newExplorerTestDep(t)
	defer client.Close()

	stateBytes, err := json.Marshal(&workflows.RemoteDownloadTaskState{
		SrcUri: "magnet:?xt=urn:btih:test",
		Dst:    "cloudreve://my/files",
		Phase:  workflows.RemoteDownloadTaskPhaseMonitor,
	})
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}

	model, err := client.Task.Create().
		SetType(queue.RemoteDownloadTaskType).
		SetStatus(enttask.StatusSuspending).
		SetUserID(user.ID).
		SetCorrelationID(uuid.Must(uuid.NewV4())).
		SetPublicState(&types.TaskPublicState{}).
		SetPrivateState(string(stateBytes)).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	model, err = client.Task.Query().Where(enttask.ID(model.ID)).WithUser().Only(context.Background())
	if err != nil {
		t.Fatalf("reload task: %v", err)
	}
	restored, err := queue.NewTaskFromModel(model)
	if err != nil {
		t.Fatalf("restore task: %v", err)
	}
	dep.TaskRegistry().Set(model.ID, restored)

	if err := CancelDownloadTask(newExplorerContext(dep, user), model.ID); err != nil {
		t.Fatalf("CancelDownloadTask: %v", err)
	}

	persisted, err := client.Task.Get(context.Background(), model.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if persisted.Status != enttask.StatusCanceled {
		t.Fatalf("persisted status = %q, want %q", persisted.Status, enttask.StatusCanceled)
	}
	if _, found := dep.TaskRegistry().Get(model.ID); found {
		t.Fatal("canceled task should be removed from registry")
	}
}
