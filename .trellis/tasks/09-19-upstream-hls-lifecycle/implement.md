# 执行计划

1. [x] 备份配置、Trellis 0.6.17 覆盖升级与迁移；官方查询 Go 版本。
2. [x] 新建集成分支，合并固定后端和前端上游，解决冲突、重生成 Ent。
3. [x] 完整读取相关代码后实施 HLS 清理、回收站/授权/时效和编号修复；补回归测试。
4. [x] 同步 Go、Docker、CI 版本，运行后端全量/race及前端检查构建。
5. [x] 审核 diff 与敏感数据；更新项目规范、记录结果并完成本地合并提交。无推送/部署。

验证：go test -count=1 ./...；go test -race -count=1 ./pkg/queue ./service/hls ./service/video ./inventory ./pkg/filemanager/fs/dbfs；前端按 package.json 的测试与构建命令。测试用临时数据，不访问生产。

类型检查已执行但失败于历史基线，详见 verification.md；任务不做全绿归档。
