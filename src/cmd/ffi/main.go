// OShinD - Foreign Function Interface
package main

// #include <stdlib.h>
import "C"

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"github.com/mogumc/oshind/pkg/downloader"
	"github.com/mogumc/oshind/types"
)

var (
	version    = "1.0.0"
	engine     *downloader.Engine
	engineOnce sync.Once
)

// DownloadOptionsJSON 下载选项 JSON（所有字段可选）
type DownloadOptionsJSON struct {
	OutputDir     string            `json:"output_dir,omitempty"`      // 输出目录
	Connections   int               `json:"connections,omitempty"`     // 最大并发连接数
	ChunkSize     int64             `json:"chunk_size,omitempty"`      // 分片大小（字节）
	Timeout       int               `json:"timeout,omitempty"`         // 请求超时（秒）
	Retry         int               `json:"retry,omitempty"`           // 重试次数
	NoResume      bool              `json:"no_resume,omitempty"`       // 禁用断点续传
	Headers       map[string]string `json:"headers,omitempty"`         // 自定义请求头
	MultiSources  []string          `json:"multi_sources,omitempty"`   // 多来源 URL
	ChecksumType  string            `json:"checksum_type,omitempty"`   // 校验类型
	ChecksumValue string            `json:"checksum_value,omitempty"`  // 期望校验和
	AutoChecksum  *bool             `json:"auto_checksum,omitempty"`   // 是否自动校验（指针区分"未设置"和"设置为false"）
	SkipTLSVerify bool              `json:"skip_tls_verify,omitempty"` // 跳过 TLS 验证
}

// ChunkStatusJSON 分片状态 JSON 结构
type ChunkStatusJSON struct {
	Index      int               `json:"index"`
	Start      int64             `json:"start"`
	End        int64             `json:"end"`
	Status     string            `json:"status"`
	Downloaded int64             `json:"downloaded"`
	Speed      float64           `json:"speed"`             // 当前分片下载速度（字节/秒）
	Headers    map[string]string `json:"headers,omitempty"` // 下载请求头
	RetryCount int               `json:"retry_count"`       // 重试次数
	Error      string            `json:"error,omitempty"`   // 错误信息
}

// TaskStatusJSON 任务状态 JSON 结构
type TaskStatusJSON struct {
	ID              string            `json:"id"`
	URL             string            `json:"url"`
	FileName        string            `json:"file_name"`
	Status          string            `json:"status"`
	Progress        float64           `json:"progress"`
	Speed           float64           `json:"speed"` // 总速度（所有分片速度之和）
	Downloaded      int64             `json:"downloaded"`
	Total           int64             `json:"total"`
	Chunks          []ChunkStatusJSON `json:"chunks"`
	Protocol        string            `json:"protocol"`         // 协议类型
	MultiSource     bool              `json:"multi_source"`     // 是否多来源下载
	ActiveThreads   int32             `json:"active_threads"`   // 当前活跃线程数
	RemainingChunks int32             `json:"remaining_chunks"` // 剩余分块数
	FailedChunks    int32             `json:"failed_chunks"`    // 失败分块数
	MaxConnections  int               `json:"max_connections"`  // 最大并发连接数
	ChunkSize       int64             `json:"chunk_size"`       // 分片大小（字节）
	TempSize        int64             `json:"temp_size"`        // temp 文件已写入大小（字节）
	CreatedAt       string            `json:"created_at"`       // 创建时间
	UpdatedAt       string            `json:"updated_at"`       // 更新时间
	Error           string            `json:"error,omitempty"`  // 任务级错误信息（失败时非空）
}

// initEngine 初始化下载引擎（单例）
func initEngine() *downloader.Engine {
	engineOnce.Do(func() {
		engine = downloader.NewEngine(nil)
	})
	return engine
}

// applyDownloadOptions 将 JSON 选项应用到 DownloadConfig
func applyDownloadOptions(config *types.DownloadConfig, opts *DownloadOptionsJSON) {
	if opts == nil {
		return
	}
	if opts.OutputDir != "" {
		config.OutputDir = opts.OutputDir
	}
	if opts.Connections > 0 {
		config.MaxConnections = opts.Connections
	}
	if opts.ChunkSize > 0 {
		config.ChunkSize = opts.ChunkSize
	}
	if opts.Timeout > 0 {
		config.Timeout = time.Duration(opts.Timeout) * time.Second
	}
	if opts.Retry >= 0 {
		config.Retry = opts.Retry
	}
	config.NoResume = opts.NoResume
	if opts.Headers != nil {
		config.Headers = opts.Headers
	}
	if len(opts.MultiSources) > 0 {
		config.MultiSources = opts.MultiSources
	}
	if opts.ChecksumType != "" {
		config.ChecksumType = opts.ChecksumType
	}
	if opts.ChecksumValue != "" {
		config.ChecksumValue = opts.ChecksumValue
	}
	if opts.AutoChecksum != nil {
		config.AutoChecksum = *opts.AutoChecksum
	}
	if opts.SkipTLSVerify && config.TLSConfig != nil {
		config.TLSConfig.InsecureSkipVerify = true
	}
}

// buildTaskStatusJSON 构建任务状态 JSON（内部函数，避免重复代码）
func buildTaskStatusJSON(task *types.DownloadTask) TaskStatusJSON {
	totalSpeed := task.Progress.CalculateSpeed()

	snapshots := task.GetChunkSnapshots()
	chunks := make([]ChunkStatusJSON, len(snapshots))
	for i, snap := range snapshots {
		chunks[i] = ChunkStatusJSON{
			Index:      snap.Index,
			Start:      snap.Start,
			End:        snap.End,
			Status:     snap.Status.String(),
			Downloaded: snap.Downloaded,
			Headers:    snap.Headers,
			RetryCount: snap.RetryCount,
		}
		if snap.Error != nil {
			chunks[i].Error = snap.Error.Error()
		}
	}

	progressPct := 0.0
	if task.Metadata.Size > 0 {
		progressPct = float64(task.Progress.GetDownloaded()) / float64(task.Metadata.Size) * 100
	}

	status := TaskStatusJSON{
		ID:              task.ID,
		URL:             task.URL,
		FileName:        task.FileName,
		Status:          task.GetStatus().String(),
		Progress:        progressPct,
		Speed:           totalSpeed,
		Downloaded:      task.Progress.GetDownloaded(),
		Total:           task.Metadata.Size,
		Chunks:          chunks,
		Protocol:        task.Protocol.String(),
		MultiSource:     task.MultiSource,
		ActiveThreads:   task.Progress.GetActiveThreads(),
		RemainingChunks: task.Progress.GetRemainingChunks(),
		FailedChunks:    task.Progress.GetFailedChunks(),
		MaxConnections:  getTaskMaxConnections(task),
		ChunkSize:       getTaskChunkSize(task),
		TempSize:        getTaskTempSize(task),
		CreatedAt:       task.CreatedAt.Format(time.RFC3339),
		UpdatedAt:       task.UpdatedAt.Format(time.RFC3339),
	}

	if taskErr := task.GetError(); taskErr != nil {
		status.Error = taskErr.Error()
	}

	return status
}

// getTaskMaxConnections 获取任务最大并发连接数
func getTaskMaxConnections(task *types.DownloadTask) int {
	if task.Config != nil {
		return task.Config.MaxConnections
	}
	return 0
}

// getTaskChunkSize 获取任务分片大小
func getTaskChunkSize(task *types.DownloadTask) int64 {
	if task.Config != nil {
		return task.Config.ChunkSize
	}
	return 0
}

// getTaskTempSize 获取 temp 文件已写入大小（字节）
func getTaskTempSize(task *types.DownloadTask) int64 {
	outputPath := task.OutputPath
	if outputPath == "" && task.Config != nil {
		outputPath = filepath.Join(task.Config.OutputDir, task.FileName)
	}
	if outputPath == "" {
		return 0
	}
	tempPath := downloader.GetTempPath(outputPath)
	fi, err := os.Stat(tempPath)
	if err != nil {
		return 0
	}
	return fi.Size()
}

//export OShinD_Version
func OShinD_Version() *C.char {
	return C.CString(version)
}

// OShinD_Download 统一下载入口
// url: 下载地址
// optionsJson: JSON 格式的下载选项（可选），字段包括：
//   - output_dir: 输出目录
//   - connections: 最大并发连接数
//   - chunk_size: 分片大小（字节）
//   - timeout: 请求超时（秒）
//   - retry: 重试次数
//   - no_resume: 禁用断点续传
//   - headers: 自定义请求头 {"key":"value"}
//   - multi_sources: 多来源 URL ["url1","url2"]
//   - checksum_type: 校验类型 (md5/sha256)
//   - checksum_value: 期望校验和
//   - auto_checksum: 是否自动校验
//   - skip_tls_verify: 跳过 TLS 验证
//
// 返回 task_id，失败返回空字符串
// 输出目录不可创建等提交期失败：仍返回 task_id，任务状态为 FAILED，
// 可通过 OShinD_GetTaskStatus 的 error 字段获取原因
//
//export OShinD_Download
func OShinD_Download(url *C.char, optionsJson *C.char) *C.char {
	goURL := C.GoString(url)

	config := types.DefaultConfig()

	outputDirSpecified := false
	if optionsJson != nil {
		optsStr := C.GoString(optionsJson)
		if optsStr != "" {
			var opts DownloadOptionsJSON
			if err := json.Unmarshal([]byte(optsStr), &opts); err == nil {
				outputDirSpecified = opts.OutputDir != ""
				applyDownloadOptions(config, &opts)
			}
		}
	}

	// FFI 宿主进程（iOS/Android 等）CWD 不可控且常为只读，
	// 未显式指定 output_dir 时回落到用户下载目录，避免下载到不可写路径
	if !outputDirSpecified {
		if home, homeErr := os.UserHomeDir(); homeErr == nil && home != "" {
			config.OutputDir = filepath.Join(home, "Downloads")
		}
	}

	e := initEngine()
	taskID, err := e.SubmitDownload(goURL, config, nil)
	if err != nil && taskID == "" {
		// 提交阶段无任务产生（如协议不支持），无状态可查询
		return C.CString("")
	}
	// 提交期失败（如输出目录无法创建）也会返回 task_id，
	// 调用方通过 GetTaskStatus 的 error 字段读取失败原因
	return C.CString(taskID)
}

//export OShinD_GetTaskStatus
func OShinD_GetTaskStatus(taskID *C.char) *C.char {
	goTaskID := C.GoString(taskID)

	e := initEngine()
	task, ok := e.GetTask(goTaskID)
	if !ok {
		return C.CString("{}")
	}

	status := buildTaskStatusJSON(task)
	data, _ := json.Marshal(status)
	return C.CString(string(data))
}

//export OShinD_GetChunkStatus
func OShinD_GetChunkStatus(taskID *C.char, chunkIndex C.int) *C.char {
	goTaskID := C.GoString(taskID)

	e := initEngine()
	task, ok := e.GetTask(goTaskID)
	if !ok {
		return C.CString("{}")
	}

	// 使用线程安全快照，避免数据竞争
	snapshots := task.GetChunkSnapshots()
	idx := int(chunkIndex)
	if idx < 0 || idx >= len(snapshots) {
		return C.CString("{}")
	}

	snap := snapshots[idx]
	status := ChunkStatusJSON{
		Index:      snap.Index,
		Start:      snap.Start,
		End:        snap.End,
		Status:     snap.Status.String(),
		Downloaded: snap.Downloaded,
		Headers:    snap.Headers,
		RetryCount: snap.RetryCount,
	}
	if snap.Error != nil {
		status.Error = snap.Error.Error()
	}

	data, _ := json.Marshal(status)
	return C.CString(string(data))
}

//export OShinD_CancelTask
func OShinD_CancelTask(taskID *C.char) C.int {
	goTaskID := C.GoString(taskID)

	e := initEngine()

	if err := e.CancelTask(goTaskID); err != nil {
		return 0
	}
	return 1
}

// OShinD_PauseTask 暂停下载并返回中断状态 JSON
// 与 OShinD_GetTaskStatus 格式相同，但状态为 PAUSED
//
//export OShinD_PauseTask
func OShinD_PauseTask(taskID *C.char) *C.char {
	goTaskID := C.GoString(taskID)

	e := initEngine()

	if err := e.PauseTask(goTaskID); err != nil {
		result := TaskStatusJSON{ID: goTaskID, Status: "not_found"}
		data, _ := json.Marshal(result)
		return C.CString(string(data))
	}

	task, ok := e.GetTask(goTaskID)
	if !ok {
		result := TaskStatusJSON{ID: goTaskID, Status: "not_found"}
		data, _ := json.Marshal(result)
		return C.CString(string(data))
	}

	status := buildTaskStatusJSON(task)
	data, _ := json.Marshal(status)
	return C.CString(string(data))
}

// OShinD_ResumeTask 恢复暂停/失败的下载任务
// 返回新的 task_id JSON，格式：{"id":"xxx"} 或错误：{"error":"xxx"}
//
//export OShinD_ResumeTask
func OShinD_ResumeTask(taskID *C.char) *C.char {
	goTaskID := C.GoString(taskID)

	e := initEngine()
	newID, err := e.ResumeTask(goTaskID, nil)
	if err != nil {
		result := map[string]string{"error": err.Error()}
		data, _ := json.Marshal(result)
		return C.CString(string(data))
	}

	result := map[string]string{"id": newID}
	data, _ := json.Marshal(result)
	return C.CString(string(data))
}

//export OShinD_RemoveTask
func OShinD_RemoveTask(taskID *C.char) C.int {
	goTaskID := C.GoString(taskID)

	e := initEngine()
	if e.RemoveTask(goTaskID) {
		return 1
	}
	return 0
}

//export OShinD_FreeString
func OShinD_FreeString(str *C.char) {
	C.free(unsafe.Pointer(str))
}

// OShinStateJSON .oshin 状态文件查询结果
type OShinStateJSON struct {
	Exists    bool             `json:"exists"`
	V         int              `json:"v,omitempty"`
	URL       string           `json:"url,omitempty"`
	FileName  string           `json:"file_name,omitempty"`
	TotalSize int64            `json:"total_size,omitempty"`
	ChunkSize int64            `json:"chunk_size,omitempty"`
	ET        string           `json:"et,omitempty"`
	Chunks    []OShinChunkJSON `json:"chunks,omitempty"`
}

// OShinChunkJSON .oshin 状态文件中的分片信息
type OShinChunkJSON struct {
	ID    int   `json:"id"`
	Start int64 `json:"start"` // 起始字节位置（含）
	End   int64 `json:"end"`   // 结束字节位置（含）
}

//export OShinD_HasResumeState
func OShinD_HasResumeState(outputDir *C.char, fileName *C.char) *C.char {
	goOutputDir := C.GoString(outputDir)
	goFileName := C.GoString(fileName)

	outputPath := filepath.Join(goOutputDir, goFileName)
	oshinPath := downloader.GetOShinStatePath(outputPath)

	state, _ := downloader.LoadOShinState(oshinPath)
	if state == nil {
		result := OShinStateJSON{Exists: false}
		data, _ := json.Marshal(result)
		return C.CString(string(data))
	}

	result := OShinStateJSON{
		Exists:    true,
		V:         state.V,
		URL:       state.URL,
		FileName:  state.FileName,
		TotalSize: state.TotalSize,
		ChunkSize: state.ChunkSize,
		ET:        state.ET,
	}
	result.Chunks = make([]OShinChunkJSON, len(state.Chunks))
	for i, c := range state.Chunks {
		result.Chunks[i] = OShinChunkJSON{
			ID:    c.ID,
			Start: c.Start,
			End:   c.End,
		}
	}

	data, _ := json.Marshal(result)
	return C.CString(string(data))
}

//export OShinD_ClearResumeState
func OShinD_ClearResumeState(outputDir *C.char, fileName *C.char) C.int {
	goOutputDir := C.GoString(outputDir)
	goFileName := C.GoString(fileName)

	outputPath := filepath.Join(goOutputDir, goFileName)
	oshinPath := downloader.GetOShinStatePath(outputPath)

	if err := downloader.RemoveOShinState(oshinPath); err != nil {
		return 0
	}
	return 1
}

func main() {}
