package queue

import "sync"

type (
	// TaskRegistry is used in slave node to track in-memory stateful tasks.
	TaskRegistry interface {
		// NextID returns the next available Task ID.
		NextID() int
		// Get returns the Task by ID.
		Get(id int) (Task, bool)
		// Set sets the Task by ID.
		Set(id int, t Task)
		// Delete deletes the Task by ID.
		Delete(id int)
		// SetCancel stores a cancel callback for a given task id.
		SetCancel(id int, cancel func())
		// Cancel executes and removes the cancel callback for a given task id.
		Cancel(id int) bool
	}

	taskRegistry struct {
		tasks   map[int]Task
		current int
		mu      sync.Mutex
		// cancels holds cancel callbacks for tasks by id
		cancels map[int]func()
	}
)

// NewTaskRegistry creates a new TaskRegistry.
func NewTaskRegistry() TaskRegistry {
	return &taskRegistry{
		tasks: make(map[int]Task),
	}
}

func (r *taskRegistry) NextID() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.current++
	return r.current
}

func (r *taskRegistry) Get(id int) (Task, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	t, ok := r.tasks[id]
	return t, ok
}

func (r *taskRegistry) Set(id int, t Task) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.tasks[id] = t
}

// SetCancel stores a cancel callback for a given task id.
func (r *taskRegistry) SetCancel(id int, cancel func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancels == nil {
		r.cancels = make(map[int]func())
	}
	r.cancels[id] = cancel
}

// Cancel executes and removes the cancel callback for a given task id.
// Returns true if a cancel callback existed and was executed.
func (r *taskRegistry) Cancel(id int) bool {
	r.mu.Lock()
	cancel, ok := r.cancels[id]
	if ok {
		delete(r.cancels, id)
	}
	t, has := r.tasks[id]
	r.mu.Unlock()

	if !ok {
		return false
	}

	if has {
		if setter, ok := t.(interface{ SetCanceled() }); ok {
			setter.SetCanceled()
		}
	}

	if cancel != nil {
		cancel()
	}

	return true
}

func (r *taskRegistry) Delete(id int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.tasks, id)
	// Cleanup associated cancel callback if present
	if r.cancels != nil {
		delete(r.cancels, id)
	}
}
