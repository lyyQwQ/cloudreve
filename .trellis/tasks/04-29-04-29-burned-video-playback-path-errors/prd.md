# burned video playback path errors

## Goal

排查烧录完成后的 MP4 在浏览器双击播放时报错的问题，重点定位 `路径不存在`、`failed to get login user`、播放器 `AbortError` 和 `/api/v4/file/content` 之间的真实因果关系。

## Context

生产任务 `13126` 已经生成 MP4，数据库记录和物理文件都存在，内容接口返回过 HTTP `206`。前端控制台仍出现 `路径不存在`、`数据库操作失败 (failed to get login user)`、播放器中断和部分接口超时。该问题和远程 worker 转码完成本身分离处理。

## Requirements

* 用浏览器 Network 捕获双击播放全过程。
* 确认报错来自哪个 API 请求、请求参数、响应 code 和 correlation ID。
* 核对文件列表、文件 URL、内容 URL、登录态中间件和播放器组件卸载顺序。
* 检查生产静态资源是否为最新 assets submodule 构建结果。
* 兼容普通本地文件、烧录输出文件、旧文件和 signed content URL。

## Acceptance Criteria

* [ ] 明确 `路径不存在` 的具体接口和输入路径。
* [ ] 明确 `failed to get login user` 的请求来源和登录态原因。
* [ ] 明确播放器 `AbortError` 是根因还是后续现象。
* [ ] 生成的 burned MP4 可以在浏览器中播放或给出可验证的后端/前端修复。
* [ ] 修复后有浏览器验证记录和必要的单元测试。

## Out of Scope

* 不重新设计播放器。
* 不改动远程 worker 转码协议，除非证明播放失败源于输出文件生成参数。
