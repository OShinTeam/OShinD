// Package types 定义 OShinD 核心数据结构
package types

import (
	"crypto/rand"
	"fmt"
	"sync"
	"time"
)

// Protocol 下载协议类型
type Protocol int

const (
	ProtocolHTTP Protocol = iota
	ProtocolHTTPS
	ProtocolFTP
	ProtocolSFTP
)

// String 返回协议的字符串表示
func (p Protocol) String() string {
	switch p {
	case ProtocolHTTP:
		return "HTTP"
	case ProtocolHTTPS:
		return "HTTPS"
	case ProtocolFTP:
		return "FTP"
	case ProtocolSFTP:
		return "SFTP"
	default:
		return "UNKNOWN"
	}
}

// ParseProtocol 从字符串解析协议类型
func ParseProtocol(s string) Protocol {
	switch s {
	case "HTTP":
		return ProtocolHTTP
	case "HTTPS":
		return ProtocolHTTPS
	case "FTP":
		return ProtocolFTP
	case "SFTP":
		return ProtocolSFTP
	default:
		return ProtocolHTTP
	}
}

// ChunkStatus 分片下载状态
type ChunkStatus int

const (
	ChunkStatusPending ChunkStatus = iota
	ChunkStatusDownloading
	ChunkStatusCompleted
	ChunkStatusFailed
)

// String 返回分片状态的字符串表示
func (s ChunkStatus) String() string {
	switch s {
	case ChunkStatusPending:
		return "PENDING"
	case ChunkStatusDownloading:
		return "DOWNLOADING"
	case ChunkStatusCompleted:
		return "COMPLETED"
	case ChunkStatusFailed:
		return "FAILED"
	default:
		return "UNKNOWN"
	}
}

// TaskStatus 任务状态
type TaskStatus int

const (
	TaskStatusPending TaskStatus = iota
	TaskStatusProbing
	TaskStatusDownloading
	TaskStatusVerifying
	TaskStatusCompleted
	TaskStatusFailed
	TaskStatusPaused
	TaskStatusResuming
)

// String 返回任务状态的字符串表示
func (s TaskStatus) String() string {
	switch s {
	case TaskStatusPending:
		return "PENDING"
	case TaskStatusProbing:
		return "PROBING"
	case TaskStatusDownloading:
		return "DOWNLOADING"
	case TaskStatusVerifying:
		return "VERIFYING"
	case TaskStatusCompleted:
		return "COMPLETED"
	case TaskStatusFailed:
		return "FAILED"
	case TaskStatusPaused:
		return "PAUSED"
	case TaskStatusResuming:
		return "RESUMING"
	default:
		return "UNKNOWN"
	}
}

// FileMetadata 文件元信息
type FileMetadata struct {
	Size          int64  // 文件大小（字节），-1 表示未知
	SupportResume bool   // 是否支持断点续传
	AcceptRange   string // 支持的 Range 单位（如 "bytes"）
	ContentType   string // 内容类型
	Checksum      string // 校验和值（如有）
	ChecksumType  string // 校验和类型："md5" / "sha256" / ""（未知）
	FileName      string // 服务器返回的文件名（Content-Disposition）
	FinalURL      string // 经过跳转后的最终 URL（无跳转则为空）
}

// ChunkInfo 分片信息
type ChunkInfo struct {
	Index       int               // 分片索引
	Start       int64             // 起始位置
	End         int64             // 结束位置（包含）
	Status      ChunkStatus       // 下载状态
	LocalOffset int64             // 本地已写入位置
	RetryCount  int               // 重试次数
	StartTime   time.Time         // 开始时间
	EndTime     time.Time         // 结束时间
	Error       error             // 最后一次错误
	Downloaded  int64             // 当前分片已下载字节数
	Headers     map[string]string // 下载请求头（存储实际使用的请求头）

	ConnectionID int // 连接 ID（用于工作窃取）
}

// DownloadConfig 下载配置
type DownloadConfig struct {
	MaxConnections  int               // 最大并发连接数（默认 4，限制 1-64）
	ChunkSize       int64             // 单片大小（默认 8MB，最小 64KB）
	Timeout         time.Duration     // 单次请求超时
	Retry           int               // 重试次数（默认 3）
	RetryDelay      time.Duration     // 重试间隔
	Headers         map[string]string // 自定义请求头
	OutputDir       string            // 输出目录
	TempDir         string            // 临时文件目录
	ChecksumType    string            // 校验类型（md5/sha256，空字符串表示不校验）
	ChecksumValue   string            // 期望的校验和值
	AutoChecksum    bool              // 是否自动探测并校验 MD5
	TLSConfig       *TLSConfig        // TLS 配置
	FTPConfig       *FTPConfig        // FTP/SFTP 配置
	MultiSources    []string          // 多来源 URL 列表（多域名下载同一文件）
	AdaptiveTimeout bool              // 是否启用自适应超时
	NoResume        bool              // 是否强制全新下载（忽略 .oshin 状态文件）
}

// TLSConfig TLS 配置
type TLSConfig struct {
	InsecureSkipVerify bool   // 是否跳过 TLS 证书验证
	CertFile           string // 客户端证书文件路径
	KeyFile            string // 客户端私钥文件路径
	CAFile             string // CA 证书文件路径
}

// FTPConfig FTP/SFTP 配置
type FTPConfig struct {
	Username string // 用户名
	Password string // 密码
	Port     int    // 端口号
}

// DefaultConfig 返回默认配置
func DefaultConfig() *DownloadConfig {
	return &DownloadConfig{
		MaxConnections:  4,
		ChunkSize:       8 * 1024 * 1024, // 8MB
		Timeout:         30 * time.Second,
		Retry:           3,
		RetryDelay:      1 * time.Second,
		Headers:         make(map[string]string),
		OutputDir:       ".",
		TempDir:         ".",
		AutoChecksum:    true,
		AdaptiveTimeout: true,
		TLSConfig: &TLSConfig{
			InsecureSkipVerify: false,
		},
		FTPConfig: &FTPConfig{
			Port: 0,
		},
	}
}

// ValidateConfig 验证并修正配置参数
func ValidateConfig(config *DownloadConfig) {
	if config == nil {
		return
	}
	// 限制线程数范围 1-64
	if config.MaxConnections < 1 {
		config.MaxConnections = 1
	}
	if config.MaxConnections > 64 {
		config.MaxConnections = 64
	}
	// 确保分片大小合理（最小 64KB，最大 1GB）
	const minChunkSize = 64 * 1024          // 64KB
	const maxChunkSize = 1024 * 1024 * 1024 // 1GB
	if config.ChunkSize <= minChunkSize {
		config.ChunkSize = minChunkSize
	}
	if config.ChunkSize >= maxChunkSize {
		config.ChunkSize = maxChunkSize
	}
}

// ProgressInfo 下载进度
type ProgressInfo struct {
	mu              sync.RWMutex
	Downloaded      int64     // 已下载字节
	Speed           float64   // 当前速度（字节/秒）
	StartTime       time.Time // 开始时间
	EndTime         time.Time // 结束时间（下载完成时设置）
	lastBytes       int64     // 上次采样时的已下载字节
	lastTime        time.Time // 上次采样时间
	ActiveThreads   int32     // 当前活跃线程数
	RemainingChunks int32     // 剩余分块数（含下载中和待下载）
	FailedChunks    int32     // 失败分块数
}

// AddDownloaded 增加已下载字节
func (p *ProgressInfo) AddDownloaded(n int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Downloaded += n
}

// SetDownloaded 重置已下载字节（降级单线程重下时清零重计）
func (p *ProgressInfo) SetDownloaded(n int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Downloaded = n
}

// GetDownloaded 获取已下载字节
func (p *ProgressInfo) GetDownloaded() int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.Downloaded
}

// GetSpeed 获取当前速度
func (p *ProgressInfo) GetSpeed() float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.Speed
}

// UpdateSpeed 更新速度
func (p *ProgressInfo) UpdateSpeed(speed float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Speed = speed
}

// GetActiveThreads 获取当前活跃线程数
func (p *ProgressInfo) GetActiveThreads() int32 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.ActiveThreads
}

// SetActiveThreads 设置当前活跃线程数
func (p *ProgressInfo) SetActiveThreads(n int32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ActiveThreads = n
}

// DecActiveThreads 减少活跃线程数
func (p *ProgressInfo) DecActiveThreads() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ActiveThreads > 0 {
		p.ActiveThreads--
	}
}

// IncActiveThreads 增加活跃线程数
func (p *ProgressInfo) IncActiveThreads() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ActiveThreads++
}

// GetRemainingChunks 获取剩余分块数
func (p *ProgressInfo) GetRemainingChunks() int32 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.RemainingChunks
}

// SetRemainingChunks 设置剩余分块数
func (p *ProgressInfo) SetRemainingChunks(n int32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.RemainingChunks = n
}

// GetFailedChunks 获取失败分块数
func (p *ProgressInfo) GetFailedChunks() int32 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.FailedChunks
}

// SetFailedChunks 设置失败分块数（任务级重试重算时使用，避免计数虚高）
func (p *ProgressInfo) SetFailedChunks(n int32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.FailedChunks = n
}

// IncFailedChunks 增加失败分块数
func (p *ProgressInfo) IncFailedChunks() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.FailedChunks++
}

// CalculateSpeed 计算瞬时速度（基于时间间隔采样）
func (p *ProgressInfo) CalculateSpeed() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(p.lastTime).Seconds()
	if elapsed < 0.1 {
		return p.Speed
	}

	bytesDiff := p.Downloaded - p.lastBytes
	speed := float64(bytesDiff) / elapsed

	p.lastBytes = p.Downloaded
	p.lastTime = now
	p.Speed = speed
	return speed
}

// VerifyResult 文件校验结果
type VerifyResult struct {
	Method   string // 校验方法："md5" / "sha256" / ""（未校验）
	Expected string // 期望校验和
	Actual   string // 实际校验和
	Passed   bool   // 校验是否通过
	Skipped  bool   // 是否跳过校验（无校验配置或探测不到）
}

// DownloadTask 下载任务
type DownloadTask struct {
	mu          sync.RWMutex
	ID          string          // 任务唯一标识
	URL         string          // 目标地址
	Protocol    Protocol        // 协议类型
	FileName    string          // 文件名
	OutputPath  string          // 输出文件完整路径（下载完成后填充）
	FileSize    int64           // 文件大小（下载完成后填充）
	Metadata    *FileMetadata   // 文件元信息
	Config      *DownloadConfig // 下载配置
	Chunks      []*ChunkInfo    // 分片信息
	Progress    *ProgressInfo   // 下载进度
	Status      TaskStatus      // 任务状态
	CreatedAt   time.Time       // 创建时间
	UpdatedAt   time.Time       // 更新时间
	MultiSource bool            // 是否多来源下载
	Verify      *VerifyResult   // 文件校验结果（下载完成后填充）
	Error       error           // 任务级错误信息（失败时填充，供状态查询/FFI 暴露）
}

// NewDownloadTask 创建新的下载任务
func NewDownloadTask(url string, protocol Protocol, config *DownloadConfig) *DownloadTask {
	if config == nil {
		config = DefaultConfig()
	}

	return &DownloadTask{
		ID:        generateTaskID(),
		URL:       url,
		Protocol:  protocol,
		Config:    config,
		Metadata:  &FileMetadata{Size: -1},
		Progress:  &ProgressInfo{StartTime: time.Now()},
		Status:    TaskStatusPending,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

// GetStatus 获取任务状态
func (t *DownloadTask) GetStatus() TaskStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Status
}

// SetStatus 设置任务状态
func (t *DownloadTask) SetStatus(status TaskStatus) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Status = status
	t.UpdatedAt = time.Now()
}

// GetError 获取任务级错误信息（线程安全），成功时为 nil
func (t *DownloadTask) GetError() error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Error
}

// Fail 将任务原子地置为 FAILED 并记录错误信息
// 判断、记录、置状态在同一把锁内完成，避免与 PauseTask 的 TOCTOU 竞态，
// 也避免轮询方读到 "FAILED 但 error 为 nil" 的瞬态
// 已处于 PAUSED 时为 no-op（用户主动中断优先于失败）
func (t *DownloadTask) Fail(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.Status == TaskStatusPaused {
		return
	}
	t.Error = err
	t.Status = TaskStatusFailed
	t.UpdatedAt = time.Now()
}

// SetChunkError 记录分片最后一次错误（err 为 nil 表示清除）
// 持任务锁写入，与 GetChunkSnapshots 的读取互斥，避免数据竞争
func (t *DownloadTask) SetChunkError(index int, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if index >= 0 && index < len(t.Chunks) {
		t.Chunks[index].Error = err
	}
}

// IncChunkRetry 增加分片重排队计数
// 持任务锁写入，与 GetChunkSnapshots 的读取互斥，避免数据竞争
func (t *DownloadTask) IncChunkRetry(index int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if index >= 0 && index < len(t.Chunks) {
		t.Chunks[index].RetryCount++
	}
}

// GetChunk 获取指定分片
func (t *DownloadTask) GetChunk(index int) *ChunkInfo {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if index < 0 || index >= len(t.Chunks) {
		return nil
	}
	return t.Chunks[index]
}

// UpdateChunkStatus 更新分片状态
func (t *DownloadTask) UpdateChunkStatus(index int, status ChunkStatus) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if index >= 0 && index < len(t.Chunks) {
		t.Chunks[index].Status = status
		if status == ChunkStatusDownloading {
			t.Chunks[index].StartTime = time.Now()
		} else if status == ChunkStatusCompleted || status == ChunkStatusFailed {
			t.Chunks[index].EndTime = time.Now()
		}
		t.UpdatedAt = time.Now()
	}
}

// GetCompletedChunkCount 返回已完成的分片数量
func (t *DownloadTask) GetCompletedChunkCount() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	count := 0
	for _, chunk := range t.Chunks {
		if chunk.Status == ChunkStatusCompleted {
			count++
		}
	}
	return count
}

// GetActiveChunks 返回当前 DOWNLOADING 状态的分片列表
// 用于 ProgressReporter 读取活跃分片信息，避免直接遍历 Chunks 切片造成数据竞争
func (t *DownloadTask) GetActiveChunks() []*ChunkInfo {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var active []*ChunkInfo
	for _, chunk := range t.Chunks {
		if chunk.Status == ChunkStatusDownloading {
			active = append(active, chunk)
		}
	}
	return active
}

// ChunkSnapshot 分片状态快照（线程安全的只读副本）
type ChunkSnapshot struct {
	Index      int
	Start      int64
	End        int64
	Status     ChunkStatus
	Downloaded int64
	RetryCount int
	Error      error
	Headers    map[string]string
}

// GetChunkSnapshots 返回所有分片的状态快照
// 返回深拷贝，调用方可安全读取而无需持锁
func (t *DownloadTask) GetChunkSnapshots() []ChunkSnapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	snapshots := make([]ChunkSnapshot, len(t.Chunks))
	for i, chunk := range t.Chunks {
		snapshots[i] = ChunkSnapshot{
			Index:      chunk.Index,
			Start:      chunk.Start,
			End:        chunk.End,
			Status:     chunk.Status,
			Downloaded: chunk.Downloaded,
			RetryCount: chunk.RetryCount,
			Error:      chunk.Error,
		}
		// 深拷贝 Headers map
		if chunk.Headers != nil {
			snapshots[i].Headers = make(map[string]string, len(chunk.Headers))
			for k, v := range chunk.Headers {
				snapshots[i].Headers[k] = v
			}
		}
	}
	return snapshots
}

// generateTaskID 生成任务唯一标识
func generateTaskID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%x", time.Now().Format("20060102150405"), b)
}

// ConnectionState 连接状态
type ConnectionState int

const (
	ConnectionStateIdle ConnectionState = iota
	ConnectionStateResolving
	ConnectionStateResolved
	ConnectionStateSlowStart
	ConnectionStateSteady
	ConnectionStateDone
	ConnectionStateError
)

// String 返回连接状态的字符串表示
func (s ConnectionState) String() string {
	switch s {
	case ConnectionStateIdle:
		return "IDLE"
	case ConnectionStateResolving:
		return "RESOLVING"
	case ConnectionStateResolved:
		return "RESOLVED"
	case ConnectionStateSlowStart:
		return "SLOW_START"
	case ConnectionStateSteady:
		return "STEADY"
	case ConnectionStateDone:
		return "DONE"
	case ConnectionStateError:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

// ConnectionInfo 连接信息
type ConnectionInfo struct {
	ID                int             // 连接 ID
	State             ConnectionState // 连接状态
	Speed             int64           // 当前速度（字节/秒）
	LastSpeedCheck    time.Time       // 上次速度检查时间
	LastSpeedDownload int64           // 上次速度检查时的已下载字节数
	ChunkIndex        int             // 关联的分片索引
	StartTime         time.Time       // 开始时间
	EndTime           time.Time       // 结束时间
}

// SlowStartConfig 慢启动配置
type SlowStartConfig struct {
	InitialBatchSize int           // 初始批次大小
	MaxBatchSize     int           // 最大批次大小
	StealThreshold   time.Duration // 窃取阈值（秒）
	MinChunkSize     int64         // 最小分片大小（字节）
	MinSplitSize     int64         // 最小分割大小（字节）
}

// DefaultSlowStartConfig 返回默认慢启动配置
func DefaultSlowStartConfig() *SlowStartConfig {
	return &SlowStartConfig{
		InitialBatchSize: 1,
		MaxBatchSize:     8,
		StealThreshold:   3 * time.Second,
		MinChunkSize:     512 * 1024,  // 512KB
		MinSplitSize:     1024 * 1024, // 1MB
	}
}
