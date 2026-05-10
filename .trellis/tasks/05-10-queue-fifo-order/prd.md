# 后台任务按添加顺序处理

## Goal

后台任务在同一批可执行任务中按添加到队列的先后顺序处理，避免后添加任务优先执行造成早提交任务长时间等待。

## What I Already Know

- Estrella 观察到后台任务处理顺序像是从队列最上面的最后添加任务开始。
- `pkg/queue/scheduler.go` 当前用堆调度任务，排序仅比较 `ResumeTime()`。
- `ResumeTime()` 相同的任务没有 FIFO 保证。
- `inventory/task.go` 的 `GetPendingTasks()` 在重启恢复 pending 任务时没有显式排序。
- 用户任务列表当前 cursor 分页按任务 ID 降序展示，最新任务在页面上位于前面；这个展示顺序和 worker 实际调度是两件事。

## Assumptions

- 本次要求调整 worker 实际执行顺序，不改变任务列表默认展示顺序。
- 多个 worker 并发存在时，顺序含义限定为“worker 从调度器取任务的顺序”；已经被不同 worker 同时取走的任务完成先后由任务耗时决定。
- 处于未来 `ResumeTime` 的挂起任务仍按恢复时间优先，只有同一恢复时间或同一批可执行任务需要按入队顺序处理。

## Requirements

- 新提交到队列的多个可执行任务应按入队顺序被调度器取出。
- `ResumeTime` 更早的任务仍优先于 `ResumeTime` 更晚的任务。
- `ResumeTime` 相同的任务使用入队顺序作为稳定的次级排序条件。
- 重启恢复 pending 任务时，读取顺序应稳定为创建时间升序，并用 ID 升序作为同一时间的次级排序。
- 保持现有任务状态流转、取消、挂起和重试逻辑。
- 保持任务列表页面的现有展示排序，避免把 UI 展示改成旧任务在前。

## Acceptance Criteria

- [ ] `pkg/queue` 中新增或更新测试，覆盖同一 `ResumeTime` 下按入队顺序出队。
- [ ] 现有 `ResumeTime` 早晚排序测试继续通过。
- [ ] `GetPendingTasks()` 恢复顺序显式稳定，避免依赖数据库默认顺序。
- [ ] 修改范围控制在队列调度与恢复查询附近，避免引入新的任务框架或复杂抽象。
- [ ] 运行相关 Go 测试通过。

## Out Of Scope

- 不调整任务列表页面的默认展示顺序。
- 不调整任务并发 worker 数量。
- 不修改任务完成时间排序语义。
- 不引入优先级队列、任务权重或新的后台任务架构。

## Technical Notes

- 调查记录：`.trellis/tasks/05-10-queue-fifo-order/research/queue-order-investigation.md`
- 相关规范：`.trellis/spec/backend/queue-task-lifecycle.md`
- 相关代码：`pkg/queue/scheduler.go`、`pkg/queue/scheduler_test.go`、`inventory/task.go`
