# todo-assistant

一个中文待办助手，支持终端界面、企业微信 AI 机器人长连接、提醒推送，以及基于 OpenAI 兼容接口的自然语言待办处理。

## 功能

- 自然语言创建、查询、更新、删除、完成、重开和延后待办。
- 使用 LLM 生成待办工具参数，标题只保留核心事项，时间和优先级拆到独立字段。
- 支持企业微信 AI Bot 长连接，收到聊天消息后自动处理并回复。
- 支持到期前 30 分钟和到期提醒。
- 支持长期记忆命令：`/memory list`、`/memory add <内容>`、`/memory delete <id>`。
- 本地 JSON 存储待办，SQLite 存储会话和长期记忆。

## 环境要求

- Go 1.26.3 或更高版本。
- 一个 OpenAI 兼容的 Chat Completions 接口。
- 如需企业微信模式，需要企业微信 AI Bot 的 `bot_id` 和 `secret`。

## 配置

默认配置路径是：

```powershell
$HOME\.todo-assistant\config.json
```

也可以用 `-config` 指定路径：

```powershell
todo-assistant doctor -config .\data\config.json
```

仓库提供了脱敏模板：

```powershell
Copy-Item .\data\config.example.json .\data\config.json
```

然后编辑 `data/config.json`：

```json
{
  "model": {
    "base_url": "https://example.com/v1",
    "api_key": "replace-me",
    "model": "gpt-5.5",
    "temperature": 0.2
  },
  "wecom": {
    "bot_id": "replace-me",
    "secret": "replace-me",
    "websocket_url": "wss://openws.work.weixin.qq.com",
    "home_chat_id": ""
  },
  "local": {
    "timezone": "Local",
    "data_dir": "",
    "log_level": "info"
  }
}
```

注意：`data/config.json` 已被 `.gitignore` 排除，不要提交真实密钥。

也可以通过交互式命令写入配置：

```powershell
todo-assistant config
```

## 构建

Windows 下构建：

```powershell
go build -o todo-assistant.exe ./cmd/todo-assistant
```

Linux/macOS 下构建：

```bash
go build -o todo-assistant ./cmd/todo-assistant
```

## 使用

查看配置、存储和模型连通性：

```powershell
.\todo-assistant.exe doctor -config .\data\config.json
```

启动终端界面：

```powershell
.\todo-assistant.exe tui -config .\data\config.json
```

启动企业微信长连接和提醒服务：

```powershell
.\todo-assistant.exe serve -config .\data\config.json
```

常见自然语言示例：

```text
提醒我明天下午三点交周报 优先级高
查看今天待办
把 phison 的待办截止时间改成下午五点
把喝水相关的待办都删掉
```

记忆命令示例：

```text
/memory add 我喜欢简洁直接的回答
/memory list
/memory delete abc123
/clear
```

## 数据文件

`local.data_dir` 为空时，默认使用：

```text
$HOME\.todo-assistant
```

主要文件：

- `todos.json`：待办数据。
- `agent-session.db`：模型会话上下文。
- `agent-memory.db`：长期记忆。
- `logs/todo-assistant.log`：服务日志。

## 测试

运行全部测试：

```powershell
go test ./...
```

## 代码结构

- `cmd/todo-assistant`：命令行入口。
- `internal/agent`：LLM 意图识别、工具调用和记忆命令。
- `internal/todo`：待办领域模型、存储、时间解析和工具执行。
- `internal/scheduler`：提醒调度。
- `internal/wecom`：企业微信 AI Bot 客户端。
- `internal/tui`：终端 UI。
- `third_party/wecom-aibot-go-sdk`：企业微信 SDK 本地替换依赖。
