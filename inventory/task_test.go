package inventory

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/enttest"
	enttask "github.com/cloudreve/Cloudreve/v4/ent/task"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/boolset"
	"github.com/cloudreve/Cloudreve/v4/pkg/conf"
	"github.com/gofrs/uuid"
)

func TestGetPendingTasksOrdersByCreatedAtThenID(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", filepath.Join(t.TempDir(), "task_pending_order.db"))
	defer client.Close()

	user := createTaskOwnerFixture(t, ctx, client)
	taskClient := NewTaskClient(client, conf.SQLiteDB, nil)
	baseTime := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)

	newer := createTaskFixture(t, ctx, client, user.ID, "target", enttask.StatusQueued, baseTime.Add(time.Minute))
	firstSameTime := createTaskFixture(t, ctx, client, user.ID, "target", enttask.StatusQueued, baseTime)
	older := createTaskFixture(t, ctx, client, user.ID, "target", enttask.StatusQueued, baseTime.Add(-time.Minute))
	secondSameTime := createTaskFixture(t, ctx, client, user.ID, "target", enttask.StatusQueued, baseTime)
	createTaskFixture(t, ctx, client, user.ID, "target", enttask.StatusCompleted, baseTime.Add(-2*time.Minute))
	createTaskFixture(t, ctx, client, user.ID, "other", enttask.StatusQueued, baseTime.Add(-3*time.Minute))

	tasks, err := taskClient.GetPendingTasks(ctx, "target")
	if err != nil {
		t.Fatalf("GetPendingTasks failed: %v", err)
	}

	wantIDs := []int{older.ID, firstSameTime.ID, secondSameTime.ID, newer.ID}
	if len(tasks) != len(wantIDs) {
		t.Fatalf("pending task count = %d, want %d", len(tasks), len(wantIDs))
	}
	for i, wantID := range wantIDs {
		if tasks[i].ID != wantID {
			t.Fatalf("task[%d] ID = %d, want %d", i, tasks[i].ID, wantID)
		}
	}
}

func createTaskOwnerFixture(t *testing.T, ctx context.Context, client *ent.Client) *ent.User {
	t.Helper()

	group, err := client.Group.Create().
		SetName("task-order").
		SetPermissions(&boolset.BooleanSet{}).
		Save(ctx)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}

	user, err := client.User.Create().
		SetEmail("task-order@example.com").
		SetNick("task-order").
		SetGroupUsers(group.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	return user
}

func createTaskFixture(t *testing.T, ctx context.Context, client *ent.Client, userID int, taskType string, status enttask.Status, createdAt time.Time) *ent.Task {
	t.Helper()

	task, err := client.Task.Create().
		SetType(taskType).
		SetStatus(status).
		SetPublicState(&types.TaskPublicState{}).
		SetUserTasks(userID).
		SetCorrelationID(uuid.Must(uuid.NewV4())).
		SetCreatedAt(createdAt).
		Save(ctx)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	return task
}
