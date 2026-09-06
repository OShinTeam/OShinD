package downloader

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/mogumc/oshind/types"
)

type Engine struct {
	mu             sync.RWMutex
	tasks          map[string]*types.DownloadTask
	cancelFuncs    map[string]context.CancelFunc
	httpDownloader *HTTPDownloader
	ftpDownloader  *FTPDownloader
	sftpDownloader *SFTPDownloader
	verifier       *Verifier
}

// NewEngine 创建新的下载引擎
func NewEngine(config *types.DownloadConfig) *Engine {
	if config == nil {
		config = types.DefaultConfig()
	}

	return &Engine{
		tasks:          make(map[string]*types.DownloadTask),
		cancelFuncs:    make(map[string]context.CancelFunc),
		httpDownloader: NewHTTPDownloader(config),
		ftpDownloader:  NewFTPDownloader(),
		sftpDownloader: NewSFTPDownloader(),
		verifier:       NewVerifier(),
	}
}

// Download 创建并执行下载任务
func (e *Engine) Download(ctx context.Context, rawURL string, config *types.DownloadConfig, onReady func(*types.DownloadTask)) (*types.DownloadTask, error) {
	if config == nil {
		config = types.DefaultConfig()
	}

	types.ValidateConfig(config)

	protocol, err := DetectProtocol(rawURL)
	if err != nil {
		return nil, fmt.Errorf("unsupported protocol: %w", err)
	}

	task := types.NewDownloadTask(rawURL, protocol, config)
	task.FileName = ExtractFileInfo(rawURL)

	// 独立的可取消 context，不受父 ctx 影响
	taskCtx, taskCancel := context.WithCancel(ctx)

	e.mu.Lock()
	e.tasks[task.ID] = task
	e.cancelFuncs[task.ID] = taskCancel
	e.mu.Unlock()

	// 尽早通知外部任务就绪，使 ProgressReporter 能观察到所有状态变化
	// （Probing → Resuming → Downloading → Verifying → Completed）
	if onReady != nil {
		onReady(task)
	}

	// 提交前校验输出目录：不存在则自动创建，避免下载阶段因路径不存在而直接失败
	if dirErr := EnsureOutputDir(config.OutputDir); dirErr != nil {
		e.failTask(task, dirErr)
		return task, dirErr
	}

	// 根据协议执行下载
	switch protocol {
	case types.ProtocolHTTP, types.ProtocolHTTPS:
		task.SetStatus(types.TaskStatusProbing)
		metadata, err := Probe(rawURL, config)
		if err != nil {
			probeErr := fmt.Errorf("probe failed: %w", err)
			e.failTask(task, probeErr)
			return task, probeErr
		}
		task.Metadata = metadata

		// 若发生跳转，使用最终 URL 进行下载
		if metadata.FinalURL != "" {
			task.URL = metadata.FinalURL
		}

		if metadata.FileName != "" {
			task.FileName = metadata.FileName
		}

		outputPath := e.getOutputPath(task)
		task.SetStatus(types.TaskStatusVerifying)
		skip, checkedPath, verifyResult, checkErr := checkExistingFile(outputPath, task)
		if checkErr != nil {
			e.failTask(task, checkErr)
			return task, checkErr
		}
		if skip {
			task.Verify = verifyResult
			task.OutputPath = checkedPath
			task.FileSize = task.Metadata.Size
			task.SetStatus(types.TaskStatusCompleted)
			e.cleanupCancelFunc(task.ID)
			return task, nil
		}
		if checkedPath != outputPath {
			task.FileName = filepath.Base(checkedPath)
		}

		if err := e.httpDownloader.Download(taskCtx, task); err != nil {
			dlErr := fmt.Errorf("HTTP download failed: %w", err)
			e.failTask(task, dlErr)
			return task, dlErr
		}
	case types.ProtocolFTP:
		if err := e.ftpDownloader.Download(taskCtx, task); err != nil {
			dlErr := fmt.Errorf("FTP download failed: %w", err)
			e.failTask(task, dlErr)
			return task, dlErr
		}

	case types.ProtocolSFTP:
		if err := e.sftpDownloader.Download(taskCtx, task); err != nil {
			dlErr := fmt.Errorf("SFTP download failed: %w", err)
			e.failTask(task, dlErr)
			return task, dlErr
		}

	default:
		protoErr := fmt.Errorf("unsupported protocol: %v", protocol)
		e.failTask(task, protoErr)
		return task, protoErr
	}

	task.SetStatus(types.TaskStatusVerifying)
	outputPath := e.getOutputPath(task)
	verifyResult := VerifyTask(task, outputPath)
	task.Verify = verifyResult
	if !verifyResult.Passed && !verifyResult.Skipped {
		verifyErr := fmt.Errorf("checksum verification failed: expected %s, got %s", verifyResult.Expected, verifyResult.Actual)
		e.failTask(task, verifyErr)
		return task, verifyErr
	}

	task.OutputPath = outputPath
	task.FileSize = task.Metadata.Size

	task.SetStatus(types.TaskStatusCompleted)
	e.cleanupCancelFunc(task.ID)

	return task, nil
}

// SubmitDownload 异步提交下载任务
// 提交阶段即校验输出目录：失败时任务被置为 FAILED 并携带 error，
// 同时返回任务 ID，调用方仍可通过 GetTask/状态查询获取失败原因
func (e *Engine) SubmitDownload(rawURL string, config *types.DownloadConfig, onReady func(*types.DownloadTask)) (string, error) {
	if config == nil {
		config = types.DefaultConfig()
	}

	types.ValidateConfig(config)

	protocol, err := DetectProtocol(rawURL)
	if err != nil {
		return "", fmt.Errorf("unsupported protocol: %w", err)
	}

	task := types.NewDownloadTask(rawURL, protocol, config)
	task.FileName = ExtractFileInfo(rawURL)

	taskCtx, taskCancel := context.WithCancel(context.Background())

	e.mu.Lock()
	e.tasks[task.ID] = task
	e.cancelFuncs[task.ID] = taskCancel
	e.mu.Unlock()

	// 只要状态就绪就通知任务准备完成
	if onReady != nil {
		onReady(task)
	}

	// 提交前校验输出目录：不存在则自动创建
	if dirErr := EnsureOutputDir(config.OutputDir); dirErr != nil {
		e.failTask(task, dirErr)
		return task.ID, dirErr
	}

	go func() {
		// 根据协议执行下载
		switch protocol {
		case types.ProtocolHTTP, types.ProtocolHTTPS:
			task.SetStatus(types.TaskStatusProbing)
			metadata, probeErr := Probe(rawURL, config)
			if probeErr != nil {
				e.failTask(task, fmt.Errorf("probe failed: %w", probeErr))
				return
			}
			task.Metadata = metadata

			// 若发生跳转，使用最终 URL 进行下载
			if metadata.FinalURL != "" {
				task.URL = metadata.FinalURL
			}

			if metadata.FileName != "" {
				task.FileName = metadata.FileName
			}

			// 下载前检查已存在文件（NoResume 时跳过）
			outputPath := e.getOutputPath(task)
			if !task.Config.NoResume {
				task.SetStatus(types.TaskStatusVerifying)
				skip, checkedPath, verifyResult, checkErr := checkExistingFile(outputPath, task)
				if checkErr != nil {
					e.failTask(task, checkErr)
					return
				}
				if skip {
					task.Verify = verifyResult
					task.OutputPath = checkedPath
					task.FileSize = task.Metadata.Size
					task.SetStatus(types.TaskStatusCompleted)
					e.cleanupCancelFunc(task.ID)
					return
				}
				if checkedPath != outputPath {
					task.FileName = filepath.Base(checkedPath)
				}
			}

			if dlErr := e.httpDownloader.Download(taskCtx, task); dlErr != nil {
				e.failTask(task, fmt.Errorf("HTTP download failed: %w", dlErr))
				return
			}

		case types.ProtocolFTP:
			if dlErr := e.ftpDownloader.Download(taskCtx, task); dlErr != nil {
				e.failTask(task, fmt.Errorf("FTP download failed: %w", dlErr))
				return
			}

		case types.ProtocolSFTP:
			if dlErr := e.sftpDownloader.Download(taskCtx, task); dlErr != nil {
				e.failTask(task, fmt.Errorf("SFTP download failed: %w", dlErr))
				return
			}
		}

		task.SetStatus(types.TaskStatusVerifying)
		outputPath := e.getOutputPath(task)
		verifyResult := VerifyTask(task, outputPath)
		task.Verify = verifyResult
		if !verifyResult.Passed && !verifyResult.Skipped {
			e.failTask(task, fmt.Errorf("checksum verification failed: expected %s, got %s", verifyResult.Expected, verifyResult.Actual))
			return
		}

		task.OutputPath = outputPath
		task.FileSize = task.Metadata.Size

		task.SetStatus(types.TaskStatusCompleted)
		e.cleanupCancelFunc(task.ID)
	}()

	return task.ID, nil
}

// GetTask 获取任务
func (e *Engine) GetTask(id string) (*types.DownloadTask, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	task, ok := e.tasks[id]
	return task, ok
}

// ListTasks 列出所有任务
func (e *Engine) ListTasks() []*types.DownloadTask {
	e.mu.RLock()
	defer e.mu.RUnlock()

	tasks := make([]*types.DownloadTask, 0, len(e.tasks))
	for _, task := range e.tasks {
		tasks = append(tasks, task)
	}
	return tasks
}

// RemoveTask 移除任务
func (e *Engine) RemoveTask(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	if _, ok := e.tasks[id]; ok {
		if cancel, ok := e.cancelFuncs[id]; ok {
			cancel()
			delete(e.cancelFuncs, id)
		}
		delete(e.tasks, id)
		return true
	}
	return false
}

// PauseTask 暂停任务
// 取消下载并将状态设置为 PAUSED
func (e *Engine) PauseTask(id string) error {
	e.mu.RLock()
	cancel, ok := e.cancelFuncs[id]
	task, taskOk := e.tasks[id]
	e.mu.RUnlock()

	if !ok || !taskOk {
		return fmt.Errorf("task %s not found or already completed", id)
	}

	// 先设置 PAUSED 再取消，http.go 检测到 ctx.Done() 时看到的是 PAUSED 而非 FAILED
	task.SetStatus(types.TaskStatusPaused)

	cancel()

	return nil
}

// ResumeTask 恢复暂停/失败的任务，移除旧任务后重新提交下载
func (e *Engine) ResumeTask(id string, onReady func(*types.DownloadTask)) (string, error) {
	e.mu.RLock()
	task, ok := e.tasks[id]
	e.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("task %s not found", id)
	}

	status := task.GetStatus()
	if status != types.TaskStatusPaused && status != types.TaskStatusFailed {
		return "", fmt.Errorf("task %s is not resumable (status: %s)", id, status)
	}

	// 标记为恢复中（FFI 轮询时可见）
	task.SetStatus(types.TaskStatusResuming)

	// RemoveTask 会清理 task 引用，先保存原始 URL 和 Config
	rawURL := task.URL
	config := task.Config

	// 移除旧任务后重新提交，.oshin 状态文件会被自动检测并恢复
	e.RemoveTask(id)

	newID, err := e.SubmitDownload(rawURL, config, onReady)
	if err != nil {
		return "", fmt.Errorf("resume failed: %w", err)
	}

	return newID, nil
}

// CancelTask 取消任务
func (e *Engine) CancelTask(id string) error {
	e.mu.RLock()
	cancel, ok := e.cancelFuncs[id]
	e.mu.RUnlock()
	if !ok {
		return fmt.Errorf("task %s not found or already completed", id)
	}
	cancel()
	return nil
}

// cleanupCancelFunc 任务结束后释放取消函数引用
// 先 cancel 再删除，避免任务提前失败时遗漏 context 资源
func (e *Engine) cleanupCancelFunc(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if cancel, ok := e.cancelFuncs[id]; ok {
		cancel()
		delete(e.cancelFuncs, id)
	}
}

// failTask 统一处理任务失败：原子记录错误并置 FAILED，再释放取消函数
// PAUSED（用户主动中断）由 DownloadTask.Fail 内部判定，不记为失败错误
func (e *Engine) failTask(task *types.DownloadTask, err error) {
	task.Fail(err)
	e.cleanupCancelFunc(task.ID)
}

// EnsureOutputDir 校验输出目录：不存在则递归创建，存在但不是目录则报错
// 在下载开始前调用，避免任务因输出路径缺失而立即失败
func EnsureOutputDir(dir string) error {
	if dir == "" || dir == "." {
		return nil
	}

	info, err := os.Stat(dir)
	switch {
	case err == nil:
		if !info.IsDir() {
			return fmt.Errorf("output path is not a directory: %s", dir)
		}
		return nil
	case os.IsNotExist(err):
		if mkErr := os.MkdirAll(dir, 0755); mkErr != nil {
			return fmt.Errorf("failed to create output dir %q: %w", dir, mkErr)
		}
		return nil
	default:
		return fmt.Errorf("failed to access output dir %q: %w", dir, err)
	}
}

// getOutputPath 获取输出文件路径
func (e *Engine) getOutputPath(task *types.DownloadTask) string {
	if task.FileName != "" {
		return filepath.Join(task.Config.OutputDir, task.FileName)
	}
	fileName := ExtractFileInfo(task.URL)
	return filepath.Join(task.Config.OutputDir, fileName)
}
