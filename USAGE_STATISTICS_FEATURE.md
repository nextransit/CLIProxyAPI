# Token 使用量统计持久化功能

## 功能概述

实现了 Token 使用量统计的持久化存储功能，支持服务重启后恢复历史统计数据。

## 实现内容

### 1. 后端持久化存储 (`internal/usage/store.go`)

- **FileStore**: 基于本地文件的存储实现
  - 自动保存间隔: 5分钟
  - 原子写入操作 (临时文件 + 重命名)
  - JSON 格式存储

- **PersistentLoggerPlugin**: 持久化插件
  - 继承 LoggerPlugin 的所有功能
  - 服务启动时自动加载历史数据
  - 定期自动保存数据
  - 支持手动保存/加载

### 2. API 端点

- `GET /v0/management/usage` - 获取当前统计信息
- `GET /v0/management/usage/export` - 导出统计数据
- `POST /v0/management/usage/import` - 导入统计数据
- `GET /usage.html` - 前端统计页面

### 3. 前端界面 (`assets/usage.html`)

提供了美观的统计展示界面，包括:
- 总请求数、Token 数概览
- API 密钥使用统计
- 模型使用统计
- 按天/小时统计
- 成功率/失败率分析
- 数据导出功能

### 4. 服务启动集成 (`cmd/server/main.go`)

```go
// 在工作目录初始化持久化
if err := usage.InitializePersistence(wd); err != nil {
    log.WithError(err).Warn("failed to initialize usage statistics persistence")
}
```

### 5. Web 路由 (`internal/api/server.go`)

添加了 `/usage.html` 路由，用于访问统计页面。

## 使用方法

### 1. 访问统计页面

```
http://localhost:PORT/usage.html
```

或使用 Management API:
```
GET http://localhost:PORT/v0/management/usage
Authorization: Bearer YOUR_MANAGEMENT_KEY
```

### 2. 导出统计数据

```bash
curl -H "Authorization: Bearer YOUR_KEY" \
  http://localhost:PORT/v0/management/usage/export \
  > usage_backup.json
```

### 3. 导入统计数据

```bash
curl -X POST -H "Authorization: Bearer YOUR_KEY" \
  -H "Content-Type: application/json" \
  -d @usage_backup.json \
  http://localhost:PORT/v0/management/usage/import
```

## GPT-5.4 xhigh 参数支持

GPT-5.4 模型已支持 `xhigh` 推理级别。

### 使用方法

1. **模型后缀方式**:
   ```json
   {
     "model": "gpt-5.4(xhigh)",
     "messages": [{"role": "user", "content": "Hello"}]
   }
   ```

2. **reasoning_effort 参数**:
   ```json
   {
     "model": "gpt-5.4",
     "messages": [{"role": "user", "content": "Hello"}],
     "reasoning_effort": "xhigh"
   }
   ```

### 支持的级别

GPT-5.4 支持以下推理级别:
- `none` - 禁用推理
- `low` - 低级别推理
- `medium` - 中等级别推理 (默认)
- `high` - 高级别推理
- `xhigh` - 超高级别推理

## 数据存储位置

默认存储在工作目录:
```
./usage_statistics.json
```

## 自动保存机制

- 每 5 分钟自动保存一次
- 服务正常关闭时自动保存
- 启动时自动加载历史数据

## 性能考虑

- 使用原子写入避免数据损坏
- 自动保存采用异步方式，不影响请求处理
- 数据合并时自动去重，避免重复计数

## 故障排除

### 统计数据未恢复

1. 检查 `usage_statistics.json` 文件是否存在
2. 检查文件权限
3. 查看日志中是否有加载错误

### 统计数据异常

1. 可以通过导出功能备份数据
2. 停止服务，删除 `usage_statistics.json`
3. 重新启动服务，系统会重新创建空统计
