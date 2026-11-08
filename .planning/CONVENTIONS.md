# Conventions

## 目录职责（#102，2026-11-08）

- **`logs/` 只放运行日志**（`run.ps1` 只写日志）——永不落 `.go` 文件。`.gitignore` 挡不住 `go build ./...`：go 工具照常编译磁盘上存在的包，残留 `.go` 草稿会让本机全量永远红。
- **调试/会话草稿一律进 `.scratch/`**——点前缀目录被 go 工具跳过，不参与 `./...` 编译，且已整体 gitignore。

依据：2026-10-18 ASR 复现会话草稿（`asr-recurrence-20261018-audio_observation*.go`，引用已删除的 `TurnManager`）曾留在 `logs/` 致本机全量失败（#102），已归档至 `.scratch/asr-recurrence-20261018/`。
