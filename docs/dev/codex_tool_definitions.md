# Codex 工具调用 JSON Schema 定义

本文档给出 OpenAI Codex CLI (`codex exec --json`) 当前会话中可用的全部工具的 **JSON Schema 结构**。
每个工具均使用与 OpenAI/Tool Use 协议兼容的 JSON Schema 格式。

> **捕获方式：** 让 Codex 自身打印工具定义，通过以下命令获取：
> ```bash
> echo 'Print the complete JSON Schema for every tool you have access to...' | \
>   codex exec --json --dangerously-bypass-approvals-and-sandbox \
>   --skip-git-repo-check -c model="MiniMax-M2.7"
> ```

---

## 目录

- [命令执行工具](#命令执行工具)
  - [exec_command - 执行命令](#exec_command---执行命令)
  - [write_stdin - 写入标准输入](#write_stdin---写入标准输入)
- [目标与计划工具](#目标与计划工具)
  - [create_goal - 创建目标](#create_goal---创建目标)
  - [get_goal - 获取目标状态](#get_goal---获取目标状态)
  - [update_goal - 更新目标](#update_goal---更新目标)
  - [update_plan - 更新计划](#update_plan---更新计划)
- [多媒体工具](#多媒体工具)
  - [view_image - 查看图片](#view_image---查看图片)
- [交互工具](#交互工具)
  - [request_user_input - 请求用户输入](#request_user_input---请求用户输入)
- [汇总清单](#汇总清单)

---

## 命令执行工具

### exec_command - 执行命令

在 PTY 中运行 shell 命令，返回输出或会话 ID 用于持续交互。这是 Codex 的**核心工具**，所有文件读写、代码编辑、系统操作都通过此工具执行。

```json
{
  "name": "exec_command",
  "description": "Runs a command in a PTY, returning output or a session ID for ongoing interaction.",
  "input_schema": {
    "type": "object",
    "properties": {
      "cmd": {
        "type": "string",
        "description": "Shell command to execute."
      },
      "justification": {
        "type": "string",
        "description": "User-facing approval question for `require_escalated`; omit otherwise."
      },
      "login": {
        "type": "boolean",
        "description": "True runs the shell with -l/-i semantics; false disables them. Defaults to true."
      },
      "max_output_tokens": {
        "type": "number",
        "description": "Output token budget. Defaults to 10000 tokens; larger requests may be capped by policy."
      },
      "prefix_rule": {
        "type": "array",
        "items": {
          "type": "string"
        },
        "description": "Reusable approval prefix for `cmd`, only with `sandbox_permissions: \"require_escalated\"`; for example [\"git\", \"pull\"]."
      },
      "sandbox_permissions": {
        "type": "string",
        "enum": ["use_default", "require_escalated"],
        "description": "Per-command sandbox override. Defaults to `use_default`; use `require_escalated` for unsandboxed execution."
      },
      "shell": {
        "type": "string",
        "description": "Shell binary to launch. Defaults to the user's default shell."
      },
      "tty": {
        "type": "boolean",
        "description": "True allocates a PTY for the command; false or omitted uses plain pipes."
      },
      "workdir": {
        "type": "string",
        "description": "Working directory for the command. Defaults to the turn cwd."
      },
      "yield_time_ms": {
        "type": "number",
        "description": "Wait before yielding output. Defaults to 10000 ms; effective range is 250-30000 ms."
      }
    },
    "required": ["cmd"],
    "additionalProperties": false
  }
}
```

**参数说明：**

| 参数 | 类型 | 必填 | 描述 |
|------|------|------|------|
| `cmd` | string | 是 | 要执行的 shell 命令 |
| `justification` | string | 否 | `require_escalated` 模式下的用户审批提示 |
| `login` | boolean | 否 | 是否使用 login shell（默认 true） |
| `max_output_tokens` | number | 否 | 输出 token 预算（默认 10000） |
| `prefix_rule` | string[] | 否 | `require_escalated` 下的命令前缀白名单 |
| `sandbox_permissions` | string | 否 | 沙箱权限覆盖：`use_default` 或 `require_escalated` |
| `shell` | string | 否 | Shell 二进制路径（默认用户 shell） |
| `tty` | boolean | 否 | 是否分配 PTY（默认 false） |
| `workdir` | string | 否 | 工作目录（默认当前 turn 目录） |
| `yield_time_ms` | number | 否 | 输出等待时间（默认 10000ms，范围 250-30000ms） |

---

### write_stdin - 写入标准输入

向已有的统一执行会话写入字符并返回近期输出。用于与长时间运行的交互式命令进行后续交互。

```json
{
  "name": "write_stdin",
  "description": "Writes characters to an existing unified exec session and returns recent output.",
  "input_schema": {
    "type": "object",
    "properties": {
      "chars": {
        "type": "string",
        "description": "Bytes to write to stdin. Defaults to empty, which polls without writing."
      },
      "max_output_tokens": {
        "type": "number",
        "description": "Output token budget. Defaults to 10000 tokens; larger requests may be capped by policy."
      },
      "session_id": {
        "type": "number",
        "description": "Identifier of the running unified exec session."
      },
      "yield_time_ms": {
        "type": "number",
        "description": "Wait before yielding output. Non-empty writes default to 250 ms and cap at 30000 ms; empty polls wait 5000-300000 ms by default."
      }
    },
    "required": ["session_id"],
    "additionalProperties": false
  }
}
```

**参数说明：**

| 参数 | 类型 | 必填 | 描述 |
|------|------|------|------|
| `session_id` | number | 是 | 运行中执行会话的标识符 |
| `chars` | string | 否 | 写入 stdin 的内容（空则仅轮询） |
| `max_output_tokens` | number | 否 | 输出 token 预算（默认 10000） |
| `yield_time_ms` | number | 否 | 输出等待时间（非空写入默认 250ms，空轮询 5000-300000ms） |

---

## 目标与计划工具

### create_goal - 创建目标

仅在用户明确请求或系统/开发者指令要求时创建目标；不要从普通任务推断目标。仅在无现有目标时生效。

```json
{
  "name": "create_goal",
  "description": "Create a goal only when explicitly requested by the user or system/developer instructions; do not infer goals from ordinary tasks. Set token_budget only when an explicit token budget is requested. Fails if a goal exists; use update_goal only for status.",
  "input_schema": {
    "type": "object",
    "properties": {
      "objective": {
        "type": "string",
        "description": "Required. The concrete objective to start pursuing. This starts a new active goal only when no goal is currently defined; if a goal already exists, this tool fails."
      },
      "token_budget": {
        "type": "integer",
        "description": "Positive token budget for the new goal. Omit unless explicitly requested."
      }
    },
    "required": ["objective"],
    "additionalProperties": false
  }
}
```

**参数说明：**

| 参数 | 类型 | 必填 | 描述 |
|------|------|------|------|
| `objective` | string | 是 | 要达成的具体目标 |
| `token_budget` | integer | 否 | Token 预算（仅在明确请求时设置） |

---

### get_goal - 获取目标状态

获取当前线程的目标，包括状态、预算、token 和耗时使用量、剩余 token 预算。

```json
{
  "name": "get_goal",
  "description": "Get the current goal for this thread, including status, budgets, token and elapsed-time usage, and remaining token budget.",
  "input_schema": {
    "type": "object",
    "properties": {},
    "required": [],
    "additionalProperties": false
  }
}
```

**参数说明：** 无参数。

---

### update_goal - 更新目标

更新现有目标状态。仅用于标记目标为 `complete`（达成）或 `blocked`（阻塞）。同一阻塞条件需连续出现至少 3 次 goal turn 才能标记为 `blocked`。

```json
{
  "name": "update_goal",
  "description": "Update the existing goal. Use this tool only to mark the goal achieved or genuinely blocked. Set status to `complete` only when the objective has actually been achieved and no required work remains. Set status to `blocked` only after the same blocking condition has recurred for at least three consecutive goal turns and the agent is at an impasse.",
  "input_schema": {
    "type": "object",
    "properties": {
      "status": {
        "type": "string",
        "enum": ["complete", "blocked"],
        "description": "Required. Set to `complete` only when the objective is achieved and no required work remains. Set to `blocked` only after the same blocking condition has recurred for at least three consecutive goal turns and the agent is at an impasse."
      }
    },
    "required": ["status"],
    "additionalProperties": false
  }
}
```

**参数说明：**

| 参数 | 类型 | 必填 | 描述 |
|------|------|------|------|
| `status` | string | 是 | 目标状态：`complete` 或 `blocked` |

---

### update_plan - 更新计划

更新任务计划。提供可选说明和计划步骤列表，每个步骤包含步骤文本和状态。同一时间最多只能有一个步骤处于 `in_progress`。

```json
{
  "name": "update_plan",
  "description": "Updates the task plan. Provide an optional explanation and a list of plan items, each with a step and status. At most one step can be in_progress at a time.",
  "input_schema": {
    "type": "object",
    "properties": {
      "explanation": {
        "type": "string",
        "description": "Optional explanation for this plan update."
      },
      "plan": {
        "type": "array",
        "description": "The list of steps",
        "items": {
          "type": "object",
          "properties": {
            "status": {
              "type": "string",
              "enum": ["pending", "in_progress", "completed"],
              "description": "Step status."
            },
            "step": {
              "type": "string",
              "description": "Task step text."
            }
          },
          "required": ["step", "status"],
          "additionalProperties": false
        }
      }
    },
    "required": ["plan"],
    "additionalProperties": false
  }
}
```

**参数说明：**

| 参数 | 类型 | 必填 | 描述 |
|------|------|------|------|
| `explanation` | string | 否 | 计划更新说明 |
| `plan` | array | 是 | 步骤列表 |
| `plan[].step` | string | 是 | 步骤文本 |
| `plan[].status` | string | 是 | 步骤状态：`pending`、`in_progress`、`completed` |

---

## 多媒体工具

### view_image - 查看图片

查看本地文件系统中的图片文件。用于需要对磁盘上的图片进行视觉检查的场景。

```json
{
  "name": "view_image",
  "description": "View a local image file from the filesystem when visual inspection is needed. Use this for images already available on disk.",
  "input_schema": {
    "type": "object",
    "properties": {
      "path": {
        "type": "string",
        "description": "Local filesystem path to an image file."
      }
    },
    "required": ["path"],
    "additionalProperties": false
  }
}
```

**参数说明：**

| 参数 | 类型 | 必填 | 描述 |
|------|------|------|------|
| `path` | string | 是 | 图片文件的本地路径 |

---

## 交互工具

### request_user_input - 请求用户输入

向用户请求 1-3 个简短问题的输入并等待回复。此工具仅在 Plan 模式下可用。

```json
{
  "name": "request_user_input",
  "description": "Request user input for one to three short questions and wait for the response. This tool is only available in Plan mode.",
  "input_schema": {
    "type": "object",
    "properties": {
      "questions": {
        "type": "array",
        "description": "Questions to show the user. Prefer 1 and do not exceed 3",
        "items": {
          "type": "object",
          "properties": {
            "header": {
              "type": "string",
              "description": "Short header label shown in the UI (12 or fewer chars)."
            },
            "id": {
              "type": "string",
              "description": "Stable identifier for mapping answers (snake_case)."
            },
            "options": {
              "type": "array",
              "description": "Provide 2-3 mutually exclusive choices. Put the recommended option first and suffix its label with \"(Recommended)\". Do not include an \"Other\" option in this list; the client will add a free-form \"Other\" option automatically.",
              "items": {
                "type": "object",
                "properties": {
                  "description": {
                    "type": "string",
                    "description": "One short sentence explaining impact/tradeoff if selected."
                  },
                  "label": {
                    "type": "string",
                    "description": "User-facing label (1-5 words)."
                  }
                },
                "required": ["label", "description"],
                "additionalProperties": false
              }
            },
            "question": {
              "type": "string",
              "description": "Single-sentence prompt shown to the user."
            }
          },
          "required": ["id", "header", "question", "options"],
          "additionalProperties": false
        }
      }
    },
    "required": ["questions"],
    "additionalProperties": false
  }
}
```

**参数说明：**

| 参数 | 类型 | 必填 | 描述 |
|------|------|------|------|
| `questions` | array | 是 | 问题列表（1-3 个） |
| `questions[].id` | string | 是 | 问题标识符（snake_case） |
| `questions[].header` | string | 是 | UI 标题标签（12 字符以内） |
| `questions[].question` | string | 是 | 单句提示文本 |
| `questions[].options` | array | 是 | 2-3 个互斥选项 |
| `questions[].options[].label` | string | 是 | 选项标签（1-5 词） |
| `questions[].options[].description` | string | 是 | 选项说明（一句话） |

---

## 汇总清单

### 工具名总表

| 工具名 | 类别 | 描述 | 必填参数 |
|--------|------|------|----------|
| `exec_command` | 命令执行 | 在 PTY 中运行 shell 命令 | `cmd` |
| `write_stdin` | 命令执行 | 向运行中的会话写入标准输入 | `session_id` |
| `create_goal` | 目标管理 | 创建目标（仅在明确请求时） | `objective` |
| `get_goal` | 目标管理 | 获取当前目标及状态 | - |
| `update_goal` | 目标管理 | 标记目标为达成或阻塞 | `status` |
| `update_plan` | 计划管理 | 更新任务计划步骤 | `plan` |
| `view_image` | 多媒体 | 查看本地图片文件 | `path` |
| `request_user_input` | 交互 | 请求用户输入（仅 Plan 模式） | `questions` |

### 工具分类

| 类别 | 工具数 | 工具列表 |
|------|--------|----------|
| 命令执行 | 2 | `exec_command`, `write_stdin` |
| 目标与计划 | 4 | `create_goal`, `get_goal`, `update_goal`, `update_plan` |
| 多媒体 | 1 | `view_image` |
| 交互 | 1 | `request_user_input` |

### 关键特征

1. **Codex 没有独立的文件读写工具** — 所有文件操作都通过 `exec_command` 执行 shell 命令完成（如 `cat`、`echo >`、`apply_patch`）。
2. **内部工具在 `--json` 输出中不可见** — `create_goal`/`get_goal`/`update_goal`/`update_plan`/`view_image` 等工具由 Codex 运行时内部处理，不作为 `command_execution` 或 `function_call` item 输出到 JSONL 流中。
3. **`exec_command` 是唯一出现在 JSONL 中的工具调用** — 以 `command_execution` 类型的 item 呈现。
4. **所有 Schema 均设置 `"additionalProperties": false`** — 严格禁止额外参数。
5. **`request_user_input` 仅在 Plan 模式下可用**。

---

**总计: 8 个工具调用定义**

---

*生成于 ClawBench 项目工作目录；基于 Codex CLI v0.57.0 工具清单。*
