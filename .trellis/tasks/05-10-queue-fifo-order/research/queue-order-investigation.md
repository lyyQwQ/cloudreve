# 后台任务处理顺序调查

## 现象

后台任务期望按添加队列的先后顺序处理。当前观察到的行为像是最后添加的任务先处理。

## 本地代码调查

### 调度器

`pkg/queue/scheduler.go` 使用 `taskHeap` 保存待处理任务。`Less(i, j)` 当前只比较 `Task.ResumeTime()`：

```go
return h[i].ResumeTime() < h[j].ResumeTime()
```

当多个任务的 `ResumeTime` 相同时，堆没有稳定排序保证。普通新任务通常没有设置未来恢复时间，因此同一时间可执行的任务可能按堆内部调整后的顺序取出，无法保证 FIFO。

### 入队

`pkg/queue/queue.go` 的 `QueueTask()` 在任务不是 `suspending` 时先持久化为 `queued`，然后调用 `q.scheduler.Queue(t)`。调度器是实际决定 worker 取哪个任务的位置。

### 重启恢复

`inventory/task.go` 的 `GetPendingTasks()` 查询 `processing`、`queued`、`suspending` 状态任务，但当前查询没有显式排序。数据库返回顺序不应作为调度依据。恢复阶段应按创建顺序载入，避免重启后顺序漂移。

### 任务列表展示

`service/explorer/workflows.go` 使用 `inventory.TaskClient.List()` 返回任务列表。`inventory/task.go` 的 cursor 分页当前按 `task.ByID(sql.OrderDesc())` 返回，用户页面会显示最新任务在前。这个排序只影响列表展示，不代表 worker 执行顺序。

## 建议修改范围

1. `pkg/queue/scheduler.go`：为入队任务记录单调序号，堆排序规则调整为：先按 `ResumeTime` 升序，再按入队序号升序。
2. `pkg/queue/scheduler_test.go`：新增同一 `ResumeTime` 下连续入队后按添加顺序出队的测试。
3. `inventory/task.go`：`GetPendingTasks()` 增加 `created_at ASC, id ASC`，使重启恢复顺序稳定。
4. 如果现有测试工具方便构造数据库任务，再补充 `inventory/task.go` 对恢复查询排序的单元测试；否则以队列调度器测试作为本次核心验证。

## 验证建议

运行：

```bash
go test -count=1 ./pkg/queue -run TestFifoScheduler
```

如果修改 `inventory/task.go` 并新增对应测试，再运行：

```bash
go test -count=1 ./inventory -run Test.*Task
```
