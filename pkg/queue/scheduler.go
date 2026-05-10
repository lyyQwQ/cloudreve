package queue

import (
	"container/heap"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
)

var (
	// ErrQueueShutdown the queue is released and closed.
	ErrQueueShutdown = errors.New("queue has been closed and released")
	// ErrMaxCapacity Maximum size limit reached
	ErrMaxCapacity = errors.New("golang-queue: maximum size limit reached")
	// ErrNoTaskInQueue there is nothing in the queue
	ErrNoTaskInQueue = errors.New("golang-queue: no Task in queue")
)

type (
	Scheduler interface {
		// Queue add a new Task into the queue
		Queue(task Task) error
		// Request get a new Task from the queue
		Request() (Task, error)
		// Shutdown stop all worker
		Shutdown() error
	}
	fifoScheduler struct {
		sync.Mutex
		taskQueue taskHeap
		capacity  int
		count     int
		sequence  uint64
		exit      chan struct{}
		logger    logging.Logger
		stopOnce  sync.Once
		stopFlag  int32
	}
	scheduledTask struct {
		task     Task
		sequence uint64
	}
	taskHeap []scheduledTask
)

// Queue send Task to the buffer channel
func (s *fifoScheduler) Queue(task Task) error {
	if atomic.LoadInt32(&s.stopFlag) == 1 {
		return ErrQueueShutdown
	}

	s.Lock()
	defer s.Unlock()

	if s.capacity > 0 && s.count >= s.capacity {
		return ErrMaxCapacity
	}

	s.sequence++
	heap.Push(&s.taskQueue, scheduledTask{task: task, sequence: s.sequence})
	s.count++

	return nil
}

// Request a new Task from channel
func (s *fifoScheduler) Request() (Task, error) {
	if atomic.LoadInt32(&s.stopFlag) == 1 {
		return nil, ErrQueueShutdown
	}

	s.Lock()
	defer s.Unlock()

	if s.count == 0 {
		return nil, ErrNoTaskInQueue
	}
	if s.taskQueue.Len() == 0 || s.taskQueue[0].task.ResumeTime() > time.Now().Unix() {
		return nil, ErrNoTaskInQueue
	}

	data := heap.Pop(&s.taskQueue)
	s.count--

	return data.(scheduledTask).task, nil
}

// Shutdown the worker
func (s *fifoScheduler) Shutdown() error {
	if !atomic.CompareAndSwapInt32(&s.stopFlag, 0, 1) {
		return ErrQueueShutdown
	}

	return nil
}

// NewFifoScheduler for create new Scheduler instance
func NewFifoScheduler(queueSize int, logger logging.Logger) Scheduler {
	w := &fifoScheduler{
		taskQueue: make([]scheduledTask, 0),
		capacity:  queueSize,
		logger:    logger,
	}

	return w
}

// Implement heap.Interface
func (h taskHeap) Len() int {
	return len(h)
}

func (h taskHeap) Less(i, j int) bool {
	if h[i].task.ResumeTime() == h[j].task.ResumeTime() {
		return h[i].sequence < h[j].sequence
	}
	return h[i].task.ResumeTime() < h[j].task.ResumeTime()
}

func (h taskHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

func (h *taskHeap) Push(x any) {
	*h = append(*h, x.(scheduledTask))
}

func (h *taskHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}
