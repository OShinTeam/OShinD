# FFI 接口文档

OShinD 提供 C 兼容的 FFI 接口，用于移动端（iOS/Android）集成。

## 构建 DLL

```bash
# Windows
CGO_ENABLED=1 GOOS=windows go build -buildmode=c-shared -o oshind.dll ./cmd/ffi/

# macOS
CGO_ENABLED=1 GOOS=darwin go build -buildmode=c-shared -o oshind.dylib ./cmd/ffi/

# Linux
CGO_ENABLED=1 GOOS=linux go build -buildmode=c-shared -o oshind.so ./cmd/ffi/
```

产物：
- `oshind.dll` / `oshind.dylib` / `oshind.so` - 动态库
- `oshind.h` - C 头文件

---

## 函数列表

### 版本信息

#### `OShinD_Version`

```c
char* OShinD_Version(void)
```

返回版本号字符串。

**返回值**: 版本号，如 `"1.0.0"`

---

### 下载任务

#### `OShinD_Download`

```c
char* OShinD_Download(char* url, char* optionsJson)
```

异步启动下载任务。支持 HTTP/HTTPS/FTP/SFTP 协议，多连接分块下载，断点续传。

**参数**:
- `url` - 下载 URL
- `optionsJson` - JSON 格式的下载选项（可选，传 `NULL` 使用默认配置）

**optionsJson 字段**:
```json
{
  "output_dir": "./downloads",        // 输出目录（未指定时使用用户下载目录，不存在时自动创建）
  "connections": 4,                    // 最大并发连接数（默认 4，范围 1-64）
  "chunk_size": 8388608,              // 分片大小（字节，默认 8MB）
  "timeout": 30,                      // 请求超时（秒，默认 30）
  "retry": 3,                         // 重试次数（默认 3）
  "no_resume": false,                 // 禁用断点续传（默认 false）
  "headers": {"User-Agent": "xxx"},   // 自定义请求头
  "multi_sources": ["url1", "url2"],  // 多来源 URL
  "checksum_type": "md5",             // 校验类型（md5/sha256）
  "checksum_value": "abc123",         // 期望校验和
  "auto_checksum": true,              // 是否自动校验
  "skip_tls_verify": false            // 跳过 TLS 验证
}
```

**返回值**: 任务 ID（字符串，用于后续操作），失败返回空字符串 `""`

> 输出目录会在提交前自动创建。若目录创建失败（如父路径是文件、权限不足），
> 仍会返回任务 ID，任务状态为 `FAILED`，可通过 `OShinD_GetTaskStatus` 的
> `error` 字段获取具体原因。

**示例**:
```c
// 默认配置
char* taskID = OShinD_Download("https://example.com/file.zip", NULL);

// 自定义配置
char* options = "{\"output_dir\":\"./downloads\",\"connections\":8,\"headers\":{\"Authorization\":\"Bearer token\"}}";
char* taskID = OShinD_Download("https://example.com/file.zip", options);
```

---

### 任务控制

#### `OShinD_GetTaskStatus`

```c
char* OShinD_GetTaskStatus(char* taskID)
```

查询任务状态。

**参数**:
- `taskID` - 任务 ID

**返回值**: JSON 格式的任务状态

**JSON 结构**:
```json
{
  "id": "task-id",
  "url": "https://example.com/file.zip",
  "file_name": "file.zip",
  "status": "DOWNLOADING",
  "progress": 45.2,
  "speed": 12500000,
  "downloaded": 540000000,
  "total": 1200000000,
  "chunks": [...],
  "protocol": "HTTPS",
  "multi_source": false,
  "active_threads": 4,
  "remaining_chunks": 2,
  "failed_chunks": 0,
  "max_connections": 4,
  "chunk_size": 8388608,
  "temp_size": 540000000,
  "created_at": "2026-05-13T12:00:00Z",
  "updated_at": "2026-05-13T12:01:30Z",
  "error": "failed to create output dir \"./downloads\": permission denied"
}
```

> `error` 仅在任务失败时出现（`omitempty`），成功时为缺省字段。
> 失败原因包括：输出目录不可创建、探测失败、校验失败、全部分片失败等。
> 分片级错误见 `chunks[].error`。

**状态值**:
- `PENDING` - 待开始
- `PROBING` - 探测中
- `DOWNLOADING` - 下载中
- `RESUMING` - 恢复中
- `VERIFYING` - 校验中
- `COMPLETED` - 已完成
- `FAILED` - 失败
- `PAUSED` - 已暂停

---

#### `OShinD_GetChunkStatus`

```c
char* OShinD_GetChunkStatus(char* taskID, int chunkIndex)
```

查询分片状态。

**参数**:
- `taskID` - 任务 ID
- `chunkIndex` - 分片索引

**返回值**: JSON 格式的分片状态

**JSON 结构**:
```json
{
  "index": 0,
  "start": 0,
  "end": 8388607,
  "status": "COMPLETED",
  "downloaded": 8388608,
  "speed": 3125000,
  "retry_count": 0,
  "headers": {"Range": "bytes=0-8388607"}
}
```

---

#### `OShinD_PauseTask`

```c
char* OShinD_PauseTask(char* taskID)
```

暂停下载任务。状态将被设置为 `PAUSED`，结束下载但是不删除内存中的状态缓存，可通过 `OShinD_ResumeTask` 恢复。

**参数**:
- `taskID` - 任务 ID

**返回值**: 暂停后的任务状态 JSON（与 `OShinD_GetTaskStatus` 格式相同）

---

#### `OShinD_ResumeTask`

```c
char* OShinD_ResumeTask(char* taskID)
```

恢复暂停或失败的下载任务。系统会自动检测 `.oshin` 断点状态文件并恢复下载。

**参数**:
- `taskID` - 原任务 ID（必须处于 `PAUSED` 或 `FAILED` 状态）

**返回值**: JSON 格式的响应
```json
// 成功
{"id": "new-task-id"}

// 失败
{"error": "task xxx is not resumable (status: COMPLETED)"}
```

**注意**: 恢复后会创建新的任务（返回新的 `id`），旧任务会被移除。

---

#### `OShinD_CancelTask`

```c
int OShinD_CancelTask(char* taskID)
```

移除任务但是不删除已下载资源。

**参数**:
- `taskID` - 任务 ID

**返回值**: 1=成功, 0=失败

---

#### `OShinD_RemoveTask`

```c
int OShinD_RemoveTask(char* taskID)
```

移除任务并删除已下载资源。

**参数**:
- `taskID` - 任务 ID

**返回值**: 1=成功, 0=失败

---

### 续传状态

#### `OShinD_HasResumeState`

```c
char* OShinD_HasResumeState(char* outputDir, char* fileName)
```

查询是否有续传状态。

**参数**:
- `outputDir` - 输出目录
- `fileName` - 文件名

**返回值**: JSON 格式的续传状态

**JSON 结构**:
```json
{
  "exists": true,
  "v": 1,
  "url": "https://example.com/file.zip",
  "file_name": "file.zip",
  "total_size": 1200000000,
  "chunk_size": 8388608,
  "et": "md5:abc123",
  "chunks": [
    {"id": 0, "start": 0, "end": 8388607},
    {"id": 1, "start": 8388608, "end": 16777215}
  ]
}
```

---

#### `OShinD_ClearResumeState`

```c
int OShinD_ClearResumeState(char* outputDir, char* fileName)
```

清除续传状态。

**参数**:
- `outputDir` - 输出目录
- `fileName` - 文件名

**返回值**: 1=成功, 0=失败

---

### 内存管理

#### `OShinD_FreeString`

```c
void OShinD_FreeString(char* str)
```

释放由 FFI 函数返回的字符串内存。

**重要**: 所有返回 `char*` 的函数都需要调用此函数释放内存。

---

## Python 集成示例

### 基本封装

```python
import ctypes
import json

class OShinD:
    def __init__(self, dll_path="oshind.dll"):
        self.lib = ctypes.CDLL(dll_path)
        self._setup_signatures()
    
    def _setup_signatures(self):
        self.lib.OShinD_Version.argtypes = []
        self.lib.OShinD_Version.restype = ctypes.c_char_p
        
        self.lib.OShinD_Download.argtypes = [ctypes.c_char_p, ctypes.c_char_p]
        self.lib.OShinD_Download.restype = ctypes.c_char_p
        
        self.lib.OShinD_GetTaskStatus.argtypes = [ctypes.c_char_p]
        self.lib.OShinD_GetTaskStatus.restype = ctypes.c_char_p
        
        self.lib.OShinD_PauseTask.argtypes = [ctypes.c_char_p]
        self.lib.OShinD_PauseTask.restype = ctypes.c_char_p
        
        self.lib.OShinD_ResumeTask.argtypes = [ctypes.c_char_p]
        self.lib.OShinD_ResumeTask.restype = ctypes.c_char_p
        
        self.lib.OShinD_CancelTask.argtypes = [ctypes.c_char_p]
        self.lib.OShinD_CancelTask.restype = ctypes.c_int
        
        self.lib.OShinD_RemoveTask.argtypes = [ctypes.c_char_p]
        self.lib.OShinD_RemoveTask.restype = ctypes.c_int
    
    def version(self):
        return self.lib.OShinD_Version().decode()
    
    def download(self, url, output_dir=None, connections=None, headers=None):
        options = {}
        if output_dir:
            options["output_dir"] = output_dir
        if connections:
            options["connections"] = connections
        if headers:
            options["headers"] = headers
        
        options_json = json.dumps(options) if options else None
        task_id = self.lib.OShinD_Download(
            url.encode(),
            options_json.encode() if options_json else None
        )
        return task_id.decode() if task_id else None
    
    def get_status(self, task_id):
        raw = self.lib.OShinD_GetTaskStatus(task_id.encode())
        return json.loads(raw.decode()) if raw else {}
    
    def pause(self, task_id):
        raw = self.lib.OShinD_PauseTask(task_id.encode())
        return json.loads(raw.decode()) if raw else {}
    
    def resume(self, task_id):
        raw = self.lib.OShinD_ResumeTask(task_id.encode())
        return json.loads(raw.decode()) if raw else {}
    
    def cancel(self, task_id):
        return self.lib.OShinD_CancelTask(task_id.encode()) == 1
    
    def remove(self, task_id):
        return self.lib.OShinD_RemoveTask(task_id.encode()) == 1


# 使用示例
oshind = OShinD("oshind.dll")
print(f"版本: {oshind.version()}")

# 基本下载
task_id = oshind.download("https://example.com/file.zip")
print(f"任务 ID: {task_id}")

# 带配置下载
task_id = oshind.download(
    "https://example.com/file.zip",
    output_dir="./downloads",
    connections=8,
    headers={"Authorization": "Bearer token"}
)

# 轮询状态
import time
while True:
    status = oshind.get_status(task_id)
    print(f"进度: {status['progress']:.1f}% - {status['status']}")
    if status["status"] in ("COMPLETED", "FAILED", "PAUSED"):
        break
    time.sleep(0.5)

# 暂停并恢复
oshind.pause(task_id)
time.sleep(2)
result = oshind.resume(task_id)
if "id" in result:
    task_id = result["id"]  # 使用新的任务 ID
```

---

## iOS 集成示例

### Swift 桥接

```swift
import Foundation

class OShinD {
    private let handle: UnsafeMutableRawPointer
    
    init?(dllPath: String) {
        guard let h = dlopen(dllPath, RTLD_LAZY) else { return nil }
        handle = h
    }
    
    func download(url: String, options: [String: Any] = [:]) -> String? {
        guard let sym = dlsym(handle, "OShinD_Download") else { return nil }
        
        typealias DownloadFunc = @convention(c) (UnsafePointer<CChar>, UnsafePointer<CChar>?) -> UnsafePointer<CChar>?
        let download = unsafeBitCast(sym, to: DownloadFunc.self)
        
        let optionsJson = options.isEmpty ? nil : try? JSONSerialization.data(withJSONObject: options)
        let optionsStr = optionsJson.flatMap { String(data: $0, encoding: .utf8) }
        
        return url.withCString { urlCStr in
            optionsStr.withCString { optCStr in
                download(urlCStr, optCStr)?.pointee
            }
        }
    }
    
    func getStatus(taskID: String) -> [String: Any]? {
        guard let sym = dlsym(handle, "OShinD_GetTaskStatus") else { return nil }
        
        typealias GetStatusFunc = @convention(c) (UnsafePointer<CChar>) -> UnsafePointer<CChar>?
        let getStatus = unsafeBitCast(sym, to: GetStatusFunc.self)
        
        guard let result = taskID.withCString({ getStatus($0)?.pointee }),
              let data = String(cString: result).data(using: .utf8),
              let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any]
        else { return nil }
        
        return json
    }
}
```

---

## 错误处理

所有返回 `char*` 的函数在失败时返回空字符串 `""`。

所有返回 `int` 的函数在失败时返回 `0`。

建议在调用后检查返回值：

```python
task_id = lib.OShinD_Download(url.encode(), options.encode())
if not task_id:
    print("下载启动失败")
else:
    print(f"任务 ID: {task_id}")
```
