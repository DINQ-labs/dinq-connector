# dinq-connector API

Base URL (通过 gateway): `https://api.dinq.me/connector`

## Gateway 路由映射（前端接口）

| 前端调用 | Connector 内部 | 认证 |
|---|---|---|
| `GET /connector/auth/platforms` | `GET /auth/platforms` | 需登录 |
| `POST /connector/auth/initiate` | `POST /auth/connect` | 需登录 |
| `GET /connector/accounts` | `GET /auth/accounts` | 需登录 |

需登录的接口由 gateway 自动从 Bearer token 提取 `user_id` 注入，前端不需要手动传 `user_id`。

---

## GET /health

健康检查。

**Response**

```json
{
  "status": "ok",
  "service": "dinq-connector",
  "version": "0.1.0"
}
```

---

## GET /auth/platforms

获取所有可用平台及用户的连接状态。

**Query Parameters**

| 参数 | 必填 | 说明 |
|---|---|---|
| `user_id` | 否 | 传入则返回该用户在各平台的连接状态 |

**Response**

```json
{
  "platforms": [
    {
      "name": "gmail",
      "display_name": "Gmail",
      "auth_scheme": "oauth2",
      "connected": true,
      "status": "active"
    },
    {
      "name": "github",
      "display_name": "GitHub",
      "auth_scheme": "oauth2",
      "connected": false
    }
  ]
}
```

**status 可选值**: `active` | `initiated` | `expired` | `failed`，未连接时不返回该字段。

---

## POST /auth/connect

发起 OAuth 连接流程，返回授权跳转 URL。

**Request Body**

```json
{
  "user_id": "用户ID",
  "platform": "gmail",
  "callback_url": "https://app.dinq.me/settings/connections"
}
```

| 字段 | 必填 | 说明 |
|---|---|---|
| `user_id` | 是 | 用户 ID |
| `platform` | 是 | 平台标识（如 `gmail`, `github`, `twitter` 等） |
| `callback_url` | 否 | 授权完成后跳转的前端页面 URL |

**Response 200**

```json
{
  "redirect_url": "https://accounts.google.com/o/oauth2/v2/auth?client_id=...&scope=...&state=...",
  "status": "initiated"
}
```

前端拿到 `redirect_url` 后跳转（`window.location.href` 或 `window.open`）。用户在平台完成授权后，会自动回调并重定向到 `callback_url`：

```
https://app.dinq.me/settings/connections?status=connected&platform=gmail
```

**Response 400**

```json
{
  "error": "user_id and platform are required"
}
```

---

## GET /auth/accounts

查询用户已连接的平台账号列表。

**Query Parameters**

| 参数 | 必填 | 说明 |
|---|---|---|
| `user_id` | 是 | 用户 ID |

**Response 200**

```json
{
  "accounts": [
    {
      "id": "uuid",
      "user_id": "用户ID",
      "platform": "gmail",
      "status": "active",
      "status_reason": "",
      "account_email": "user@gmail.com",
      "token_type": "bearer",
      "scopes": "https://www.googleapis.com/auth/gmail.modify ...",
      "expires_at": "2026-03-21T10:00:00Z",
      "created_at": "2026-03-20T10:00:00Z",
      "updated_at": "2026-03-20T10:00:00Z"
    }
  ]
}
```

| 字段 | 说明 |
|---|---|
| `platform` | 平台标识 |
| `status` | `active` / `initiated` / `expired` / `failed` |
| `account_email` | 用户在该平台的邮箱（如 Gmail 地址） |
| `expires_at` | token 过期时间，`null` 表示不过期 |

---

## POST /api/execute

执行平台工具（服务间调用）。用于后端以用户身份调用已连接平台的操作，如用用户的 Gmail 发邮件。

**Request Body**

```json
{
  "user_id": "用户ID",
  "platform": "gmail",
  "action": "send_email",
  "params": {
    "to": "recipient@example.com",
    "subject": "面试邀请",
    "body": "您好，我们邀请您参加面试..."
  }
}
```

| 字段 | 必填 | 说明 |
|---|---|---|
| `user_id` | 是 | 用户 ID |
| `platform` | 是 | 平台标识 |
| `action` | 是 | 工具名称（不带平台前缀，如 `send_email` 而非 `gmail_send_email`） |
| `params` | 否 | 工具参数 |

### Gmail 可用 actions

| action | 类型 | params |
|---|---|---|
| `send_email` | WRITE | `to`\*, `subject`\*, `body`\*, `cc`, `bcc` |
| `list_messages` | READ | `q`, `max_results` |
| `get_message` | READ | `message_id`\* |
| `search_messages` | READ | `q`\*, `max_results` |
| `reply_to_email` | WRITE | `message_id`\*, `body`\*, `reply_all` |
| `create_draft` | WRITE | `to`\*, `subject`\*, `body`\*, `cc` |
| `list_labels` | READ | 无 |
| `get_thread` | READ | `thread_id`\* |
| `modify_labels` | WRITE | `message_id`\*, `add_labels`, `remove_labels` |

\* 为必填参数

**Response 200**

```json
{
  "success": true,
  "result": {
    "id": "msg-id",
    "threadId": "thread-id",
    "labelIds": ["SENT"]
  }
}
```

**Response 401** — 用户未连接该平台

```json
{
  "error": "user not connected: no connected account for user xxx on gmail"
}
```
