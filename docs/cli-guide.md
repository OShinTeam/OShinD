# CLI 使用指南

## 安装

```bash
# 从源码构建
go build -o oshind ./cmd/cli/

# Windows
go build -o oshind.exe ./cmd/cli/
```

## 命令结构

```
oshind <command> [arguments]
```

### 可用命令

| 命令 | 别名 | 说明 |
|------|------|------|
| `download` | `dl` | 下载文件 |
| `probe` | - | 探测服务器信息 |
| `has-resume` | - | 检查断点续传状态 |
| `clear-resume` | - | 清除断点续传状态 |
| `version` | `-v`, `--version` | 显示版本 |
| `help` | `-h`, `--help` | 显示帮助 |

---

## 下载文件

### 基本用法

```bash
oshind download <url> [options]
oshind dl <url> [options]
```

### 示例

```bash
# 基本下载
oshind dl https://example.com/file.zip

# 指定输出目录
oshind dl https://example.com/file.zip -o ./downloads/file.zip

# 指定输出目录（自动使用原始文件名）
oshind dl https://example.com/file.zip -o ./downloads/

# 使用 8 个并发连接
oshind dl https://example.com/file.zip -c 8

# 设置分片大小为 4MB
oshind dl https://example.com/file.zip -s 4m

# 自定义请求头
oshind dl https://example.com/file.zip -H "Authorization: Bearer token123"
oshind dl https://example.com/file.zip -H "Referer: https://example.com" -H "Cookie: session=abc"

# 强制全新下载（忽略断点续传）
oshind dl https://example.com/file.zip --no-resume

# 设置校验和
oshind dl https://example.com/file.zip --checksum-type md5 --checksum-value abc123...

# 多来源下载
oshind dl https://cdn1.example.com/file.zip -m https://cdn2.example.com/file.zip
```

### FTP/SFTP 下载

```bash
# FTP 下载
oshind dl ftp://example.com/file.zip -u username -p password

# FTP 指定端口
oshind dl ftp://example.com/file.zip -u user -p pass --ftp-port 2121

# SFTP 下载
oshind dl sftp://example.com/file.zip -u root -p password

# SFTP 跳过 TLS 验证
oshind dl sftp://example.com/file.zip -u root --skip-tls-verify
```

---

## 参数说明

### 通用参数

| 参数 | 简写 | 说明 | 默认值 |
|------|------|------|--------|
| `--output` | `-o` | 输出路径（目录不存在时自动创建） | 当前目录 |
| `--connections` | `-c` | 并发连接数 (1-64) | 4 |
| `--chunk-size` | `-s` | 分片大小 | 8m |
| `--timeout` | `-t` | 请求超时 | 30s |
| `--retry` | `-r` | 重试次数 | 3 |

### HTTP 专用参数

| 参数 | 说明 |
|------|------|
| `-H, --header <k:v>` | 自定义请求头（可重复使用） |
| `-m, --multi-source <url>` | 额外下载源（可重复使用） |
| `--no-resume` | 强制全新下载 |
| `--no-checksum` | 禁用自动校验 |
| `--checksum-type <type>` | 校验类型（md5/sha1/sha256） |
| `--checksum-value <value>` | 期望校验和值 |

### FTP/SFTP 参数

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `-u, --user` | 用户名 | - |
| `-p, --password` | 密码 | - |
| `--ftp-port` | FTP 端口 | 21 |
| `--sftp-port` | SFTP 端口 | 22 |
| `--skip-tls-verify` | 跳过 TLS 验证 | false |

### 分片大小格式

支持以下格式：
- `64k` - 64 KB
- `1m` - 1 MB
- `4m` - 4 MB
- `1g` - 1 GB
- 纯数字表示字节数

---

## 探测服务器

```bash
oshind probe <url>
```

探测服务器信息，显示：
- 文件名
- 文件大小
- 内容类型
- 是否支持断点续传
- 校验信息
- 服务器地址

### 示例

```bash
$ oshind probe https://example.com/file.zip
Probing: https://example.com/file.zip
URL:        https://example.com/file.zip
File:       file.zip
Size:       1.20 GB
Type:       application/zip
Resume:     true
Checksum:   md5:abc123def456789...
Server:     example.com:443 (https)
```

---

## 断点续传

OShinD 自动处理断点续传：

1. 下载过程中会保存状态到 `.oshin` 文件
2. 中断后重新执行相同命令，会自动检测并继续下载
3. 使用 `--no-resume` 强制重新下载

### 管理续传状态

```bash
# 检查是否有续传状态
oshind has-resume <output_dir> <file_name>

# 清除续传状态
oshind clear-resume <output_dir> <file_name>
```

### 状态文件

状态文件保存在输出文件同目录，格式为 `.oshin`：
```
downloads/
├── file.zip
└── file.zip.oshin
```

---

## 校验

### 自动校验

默认情况下，OShinD 会自动探测服务器提供的校验信息并进行校验。

### 手动指定校验和

```bash
# SHA256 校验
oshind dl https://example.com/file.zip --checksum sha256:abc123...

# 分开指定
oshind dl https://example.com/file.zip --checksum-type md5 --checksum-value abc123...

# 禁用校验
oshind dl https://example.com/file.zip --no-checksum
```

---

## 中断与控制

### Ctrl+C 暂停

按下 `Ctrl+C` 会：
1. 停止当前下载
2. 自动保存下载状态到 `.oshin` 文件
3. 可以重新执行相同命令继续下载

*暂停摘要目前暂不可用*

**暂停摘要示例**：

```
⏸️ 已暂停

─ 暂停摘要 ─

  URL:        https://example.com/file.zip
  文件:       file.zip
  大小:       1.20 GB
  已下载:     540.00 MB (44.0%)
  分片:       6/16
  校验和:     md5:abc123def456789...
  协议:       HTTPS

  ✓ 状态文件: ./downloads/file.zip.oshin
  ℹ 重新运行相同的下载命令继续下载
```

**暂停摘要包含的信息**：
- **URL** — 下载源地址
- **文件** — 输出文件名
- **大小** — 文件总大小
- **已下载** — 已下载的字节数（含百分比）
- **分片** — 已完成/总分片数
- **校验和** — 服务器提供的校验信息（如有）
- **协议** — 使用的下载协议
- **状态文件** — `.oshin` 文件的完整路径
- **续传提示** — 提示重新运行相同命令

### 进度显示

下载过程中会显示动态进度条（终端环境）：

```
  ⠋ [=====================>               ] 65.3% | 31.61 MB/s | ETA: 12s
  Threads: 4/4  |  Remaining: 8 chunks  |  Failed: 0
  ── Active Threads ──
  [T0] Chunk#3  [########........]  62.3%
  [T1] Chunk#7  [######..........]  31.1%
```

**进度条说明**：
- 第1行：spinner 动画 + 进度条 + 百分比 + 速度 + 预计剩余时间
- 第2行：线程统计（活跃线程数/总线程数、剩余分块数、失败分块数）
- 第3行起：每个活跃线程的详细进度

在非终端环境（管道/重定向）下，进度会以滚动方式输出。
