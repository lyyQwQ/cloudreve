# workflow page timeout and remote download queue churn

## Goal

排查生产后台任务页面加载慢、502 或 `/api/v4/workflow` 超时的问题，并解决旧离线下载任务高频在 `suspending` 与 `processing` 之间切换导致的日志和数据库写入压力。

## Context

生产日志显示约 23 个旧 `remote_download` 任务在短时间内大量状态切换。最近 5 分钟曾统计到约 1396 条 `RemoteDownloadQueue` 状态变化日志。后台任务页面同时出现 `/api/v4/workflow` 的 HTTP/2 ping 失败和超时。

## Requirements

* 先只读收集生产日志、DB 状态、队列配置和 qBittorrent 任务状态。
* 明确这些旧任务是否仍对应 qBittorrent 中存在的任务。
* 明确高频切换来自队列调度、`ResumeAfter`、节点 interval、旧任务恢复，还是错误重试。
* 修复方案优先限制轮询频率、减少无意义状态写入、保留取消与恢复能力。
* 后台任务页面需要恢复可用，旧任务不应持续刷数据库和日志。
* qBittorrent 在本项目中是 Cloudreve 的远程下载执行器，qB torrent 生命周期必须由 Cloudreve 管理；转存完成或关联文件被删除后，不能把做种状态长期交给 qBittorrent 自行保留。
* 删除由离线下载转存出的 Cloudreve 文件或目录时，应能清理对应的 Cloudreve 离线下载任务和 qBittorrent torrent，避免文件已删除但任务仍显示做种中。

## Acceptance Criteria

* [ ] 明确 `/api/v4/workflow` 慢或 502 的直接原因。
* [ ] 明确旧离线下载任务高频切换的根因。
* [ ] 修复后生产日志不再持续高频刷状态切换。
* [ ] 取消、恢复、完成状态仍符合离线下载任务生命周期。
* [ ] 删除已转存文件后，关联 qBittorrent 做种任务会由 Cloudreve 清理，任务列表不再显示孤立做种状态。
* [ ] 历史孤儿做种任务在后续 seeding 检查中完成自清理，仍保留 Cloudreve 文件的任务不受影响。
* [ ] 有队列单元测试或集成测试覆盖修复行为。

## Out of Scope

* 不改造 qBittorrent 为新的下载系统。
* 不清空或删除生产任务记录；如需清理，先移动或归档并经 Estrella 确认。
* 不新增独立 qB 管理后台；Cloudreve 仍是远程下载任务的唯一管理入口。
