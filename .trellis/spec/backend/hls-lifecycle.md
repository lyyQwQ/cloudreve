# HLS 生命周期与播放凭据

## 1. Scope / Trigger
修改文件删除、回收站恢复、HLS 状态/播放/直链或播放器时适用。

## 2. Signatures
- `FileClient.Delete` 在事务上下文中收集产物目录；根事务 `commit` 成功后清理。
- `GET /api/v4/hls/:fileId?uri=...` 返回 `play_url`，不公开物理目录。
- `DELETE /api/v4/hls/:fileId` 仅文件所有者且有 Files.Write scope。
- `PlaybackURL(ctx, dep, fileID, linkID)` 只能在授权之后调用。

## 3. Contracts
- 回收站保留 HLS；检查整条祖先链，禁止回收站内原始播放列表和切片。恢复时补回旧版可能清除的标记。
- 签名覆盖 escaped path 及去掉 sign 后排序编码的全部 query；不使用只签路径的通用 auth.SignURI。
- until 使用 entity URL 有效期，非正配置回退 24h；片段继承列表期限，不能刷新延长。
- v 绑定产物路径；link 绑定可撤销直链；share/sv 绑定分享权限和加密校验值，不暴露密码或裸密码哈希。分享有效期取更早者。
- 片段接受三位旧格式、segment_ 至少五位数字格式。目录仍按 fileID/生成时间隔离。
- 删除磁盘失败会日志告警；已有孤儿扫描默认 dry-run，自动清理需要 hls_reconcile_apply，不能声称默认自动兜底。

## 4. Validation & Error Matrix
- 不存在或回收站文件：404；匿名裸链接、越权、篡改/过期凭据：403。
- 撤销直链、分享改密/删除/到期/移出分享目录：旧播放凭据拒绝。
- 事务回滚及嵌套事务未提交：目录保留；外层成功提交：目录与产物记录删除。

## 5. Good / Base / Bad
- Good: segment_100000.ts 可播放且签名仍校验。
- Base: 正常所有者通过状态接口取得限时链接。
- Bad: 仅检查源文件记录存在，忽略祖先文件夹在回收站。

## 6. Tests Required
- inventory.TestHLSFileDeleteTransaction：批量、嵌套、提交和回滚。
- service/hls.TestHLSPlaybackLifecycle：匿名、过期、篡改、祖先回收、恢复、重转码、六位切片。
- service/hls.TestHLSLinkRevocation：直链撤销及去掉绑定参数、分享期限与改密。
- 前端 HLSManage / DeleteConfirmationHLS / DirectLinks 测试及生产构建。

## 7. Wrong vs Correct
Wrong：浏览器先逐个删除 HLS，再请求批量删除文件。
Correct：后端统一删除文件事务收集 HLS，外层提交后才清理目录。
Wrong：将 until/v 放入查询字符串后使用通用路径签名。
Correct：HLS 专用签名将路径和所有绑定参数作为同一签名正文。
