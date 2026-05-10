package queue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cloudreve/Cloudreve/v4/ent"
	enttask "github.com/cloudreve/Cloudreve/v4/ent/task"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
)

func TestFifoSchedulerFutureTaskDoesNotBlockDueTask(t *testing.T) {
	s := NewFifoScheduler(0, nil)
	now := time.Now().Unix()
	due := schedulerTestTask(1, now-1)
	future := schedulerTestTask(2, now+3600)

	if err := s.Queue(due); err != nil {
		t.Fatalf("queue due task: %v", err)
	}
	if err := s.Queue(future); err != nil {
		t.Fatalf("queue future task: %v", err)
	}

	got, err := s.Request()
	if err != nil {
		t.Fatalf("request task: %v", err)
	}
	if got.ID() != due.ID() {
		t.Fatalf("requested task ID = %d, want %d", got.ID(), due.ID())
	}

	if got, err := s.Request(); got != nil || !errors.Is(err, ErrNoTaskInQueue) {
		t.Fatalf("future task request = (%v, %v), want ErrNoTaskInQueue", got, err)
	}
}

func TestFifoSchedulerRequestsEarliestDueTask(t *testing.T) {
	s := NewFifoScheduler(0, nil)
	now := time.Now().Unix()
	late := schedulerTestTask(1, now+30)
	earliest := schedulerTestTask(2, now-30)
	middle := schedulerTestTask(3, now-10)

	for _, task := range []Task{late, earliest, middle} {
		if err := s.Queue(task); err != nil {
			t.Fatalf("queue task %d: %v", task.ID(), err)
		}
	}

	for _, want := range []Task{earliest, middle} {
		got, err := s.Request()
		if err != nil {
			t.Fatalf("request task: %v", err)
		}
		if got.ID() != want.ID() {
			t.Fatalf("requested task ID = %d, want %d", got.ID(), want.ID())
		}
	}
}

func TestFifoSchedulerRequestsSameResumeTimeInQueueOrder(t *testing.T) {
	s := NewFifoScheduler(0, nil)
	now := time.Now().Unix()
	first := schedulerTestTask(1, now-1)
	second := schedulerTestTask(2, now-1)
	third := schedulerTestTask(3, now-1)

	for _, task := range []Task{first, second, third} {
		if err := s.Queue(task); err != nil {
			t.Fatalf("queue task %d: %v", task.ID(), err)
		}
	}

	for _, want := range []Task{first, second, third} {
		got, err := s.Request()
		if err != nil {
			t.Fatalf("request task: %v", err)
		}
		if got.ID() != want.ID() {
			t.Fatalf("requested task ID = %d, want %d", got.ID(), want.ID())
		}
	}
}

func schedulerTestTask(id int, resumeTime int64) Task {
	return &schedulerTask{DBTask: &DBTask{
		Task: &ent.Task{
			ID:          id,
			Type:        RemoteDownloadTaskType,
			Status:      enttask.StatusSuspending,
			PublicState: &types.TaskPublicState{ResumeTime: resumeTime},
		},
	}}
}

type schedulerTask struct {
	*DBTask
}

func (t *schedulerTask) Do(context.Context) (enttask.Status, error) {
	return enttask.StatusCompleted, nil
}
