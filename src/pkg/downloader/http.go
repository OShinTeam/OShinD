package downloader

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mogumc/oshind/types"
)

// checkExistingFile 检查已存在的同名文件是否与待下载文件一致
func checkExistingFile(outputPath string, task *types.DownloadTask) (skip bool, finalPath string, verifyResult *types.VerifyResult, err error) {
	fi, statErr := os.Stat(outputPath)
	if statErr != nil || fi == nil {
		// 文件不存在，正常下载
		return false, outputPath, nil, nil
	}

	// 文件匹配语义：始终使用 probe 校验和判断文件是否一致（不受 AutoChecksum 影响）
	savedAutoChecksum := task.Config.AutoChecksum
	task.Config.AutoChecksum = true
	result := VerifyTask(task, outputPath)
	task.Config.AutoChecksum = savedAutoChecksum

	if result.Passed {
		return true, outputPath, result, nil
	}

	if !result.Skipped {
		// 校验不一致，重命名
		newPath := findAvailablePath(outputPath)
		if renameErr := os.Rename(outputPath, newPath); renameErr != nil {
			return false, "", result, fmt.Errorf("failed to rename existing file: %w", renameErr)
		}
		return false, newPath, result, nil
	}

	// VerifyTask 跳过（无校验源）：回退到文件大小比较
	if task.Metadata.Size > 0 && fi.Size() == task.Metadata.Size {
		return true, outputPath, result, nil
	}

	// 大小不一致，重命名
	newPath := findAvailablePath(outputPath)
	if renameErr := os.Rename(outputPath, newPath); renameErr != nil {
		return false, "", result, fmt.Errorf("failed to rename existing file: %w", renameErr)
	}
	return false, newPath, result, nil
}

// validateResumeFile 校验续传时临时文件的一致性
func validateResumeFile(state *OShinState, tempPath string, task *types.DownloadTask) bool {
	fi, err := os.Stat(tempPath)
	if err != nil || fi == nil {
		return false
	}

	// temp 文件大小必须与记录的总大小一致（Truncate 预分配）
	if fi.Size() != state.TotalSize {
		return false
	}

	// 下载文件校验值需要和续传文件一致
	if state.ET != "" {
		parts := strings.SplitN(state.ET, ":", 2)
		if len(parts) == 2 {
			checksumType, checksumValue := parts[0], parts[1]
			if (checksumType != task.Metadata.ChecksumType || checksumValue != task.Metadata.Checksum) && (checksumType != task.Config.ChecksumType || checksumValue != task.Config.ChecksumValue) {
				return false
			}
		}
	} else {
		// 下载地址必须存在于续传状态的 URL 列表中
		if task != nil && task.URL != "" {
			found := false
			for _, u := range state.URLs {
				if u == task.URL {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}

	return true
}

// findAvailablePath 查找可用的重命名路径
func findAvailablePath(outputPath string) string {
	for i := 1; ; i++ {
		candidate := fmt.Sprintf("%s.%d", outputPath, i)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

// downloadSingleStream 降级为单线程不分片整文件下载
// 场景：服务器不支持 Range（返回 200 或 Content-Range 错位），多分片下载必然失败
// 复用已打开的 tempFile（Windows 不允许对同一路径重复打开写入），清零后从 0 顺序写入
func (d *HTTPDownloader) downloadSingleStream(ctx context.Context, task *types.DownloadTask, tempFile *os.File) error {
	// 已下载的分片数据作废：截断为 0 重新开始
	if err := tempFile.Truncate(0); err != nil {
		return fmt.Errorf("failed to truncate temp file for single-stream fallback: %w", err)
	}
	if _, err := tempFile.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("failed to seek temp file: %w", err)
	}

	task.SetStatus(types.TaskStatusDownloading)

	// 进度清零重计：分片阶段已计入的字节数全部作废
	task.Progress.SetDownloaded(0)
	task.Progress.SetRemainingChunks(1)
	task.Chunks = []*types.ChunkInfo{
		{
			Index:  0,
			Start:  0,
			End:    -1, // 表示下载到末尾
			Status: types.ChunkStatusDownloading,
		},
	}

	req, err := http.NewRequestWithContext(ctx, "GET", task.URL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	// 不设置 Range 头：普通 GET 完整下载
	req.Header.Set("User-Agent", "OShinD/1.0")
	req.Header.Set("Content-Type", detectContentType(task.FileName))
	applyHeaders(req, task.Config.Headers)

	// 不复用 d.client（其 Timeout 覆盖整个响应体读取，大文件必然中途超时）；
	// 使用仅限制连接建立的裸客户端，响应体读取时长不限，中断经 ctx 传递
	transport := &http.Transport{}
	if d.tlsConfig != nil {
		transport.TLSClientConfig = d.tlsConfig
	}
	singleClient := &http.Client{
		Transport: transport,
		Timeout:   0,
	}

	resp, err := singleClient.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	buf := make([]byte, 32*1024)
	var offset int64
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := tempFile.WriteAt(buf[:n], offset); writeErr != nil {
				return fmt.Errorf("failed to write: %w", writeErr)
			}
			offset += int64(n)
			task.Progress.AddDownloaded(int64(n))
			task.Chunks[0].Downloaded = offset
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return fmt.Errorf("failed to read: %w", readErr)
		}
	}

	// 完整性校验：已知大小但实际接收不足视为失败
	if task.Metadata.Size > 0 && offset < task.Metadata.Size {
		return fmt.Errorf("truncated download: got %d of %d bytes", offset, task.Metadata.Size)
	}

	return nil
}

// HTTPDownloader HTTP/HTTPS 下载器
type HTTPDownloader struct {
	client         *http.Client
	transport      *http.Transport         // 主 transport（用于快速关闭空闲连接）
	clients        map[string]*http.Client // 多来源下载时，每个 URL 对应一个 client
	mu             sync.Mutex              // 保护 maxConnTime 和 fastFailClient 的并发访问
	maxConnTime    time.Duration           // 最长成功连接时间（用于自适应超时）
	fastFailClient *http.Client            // 快速失败客户端（用于快速重试）
	weightedURLs   []string                // 加权 URL 列表（主 URL 占比更高）
	urlIndex       int64                   // 当前轮询索引（原子操作）
	tlsConfig      *tls.Config             // TLS 配置（用于快速失败客户端）
}

// NewHTTPDownloader 创建新的 HTTP 下载器
func NewHTTPDownloader(config *types.DownloadConfig) *HTTPDownloader {
	if config == nil {
		config = types.DefaultConfig()
	}

	types.ValidateConfig(config)

	downloader := &HTTPDownloader{
		transport: &http.Transport{
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   config.MaxConnections,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: config.Timeout,
		},
		clients:     make(map[string]*http.Client),
		maxConnTime: config.Timeout,
	}

	// 应用 TLS 配置（跳过证书验证等）
	if config.TLSConfig != nil && config.TLSConfig.InsecureSkipVerify {
		tlsCfg := &tls.Config{
			InsecureSkipVerify: true,
		}
		downloader.transport.TLSClientConfig = tlsCfg
		downloader.tlsConfig = tlsCfg
	}

	downloader.client = &http.Client{
		Timeout:   config.Timeout,
		Transport: downloader.transport,
	}

	downloader.fastFailClient = &http.Client{
		Timeout: 3 * time.Second,
	}

	// 为多来源 URL 创建独立的 client（共享同一个 transport，便于统一关闭）
	for _, url := range config.MultiSources {
		downloader.clients[url] = &http.Client{
			Timeout:   config.Timeout,
			Transport: downloader.transport,
		}
	}

	return downloader
}

// buildWeightedURLs 构建加权 URL 列表
// 主 URL 出现 2 次，其他 URL 各出现 1 次，确保主 URL 占比高于其他 URL
func buildWeightedURLs(primary string, others []string) []string {
	if len(others) == 0 {
		return []string{primary}
	}
	urls := make([]string, 0, 2+len(others))
	urls = append(urls, primary, primary)
	urls = append(urls, others...)
	return urls
}

// nextURL 从加权 URL 列表中轮询获取下一个 URL（线程安全）
func (d *HTTPDownloader) nextURL() string {
	if len(d.weightedURLs) == 0 {
		return ""
	}
	idx := atomic.AddInt64(&d.urlIndex, 1) - 1
	return d.weightedURLs[idx%int64(len(d.weightedURLs))]
}

// Download 执行 HTTP/HTTPS 下载
func (d *HTTPDownloader) Download(ctx context.Context, task *types.DownloadTask) error {
	// 输出目录兜底校验：不存在则创建，避免创建临时文件时才失败
	if err := EnsureOutputDir(task.Config.OutputDir); err != nil {
		return err
	}

	outputPath := d.getOutputPath(task)
	oshinPath := GetOShinStatePath(outputPath)
	tempPath := GetTempPath(outputPath)

	// 尝试从 .oshin 文件恢复下载状态（除非指定了 --no-resume）
	resumedFromState := false
	var existingState *OShinState
	if !task.Config.NoResume {
		existingState, _ = LoadOShinState(oshinPath)
	}
	if existingState != nil {
		task.SetStatus(types.TaskStatusResuming)
		if task.Metadata.Size <= 0 {
			task.Metadata.Size = existingState.TotalSize
		}
		if !validateResumeFile(existingState, tempPath, task) {
			RemoveOShinState(oshinPath)
			os.Remove(tempPath)
			existingState = nil
		}
	}
	if existingState != nil && existingState.TotalSize == task.Metadata.Size {
		chunkCount := int((existingState.TotalSize + existingState.ChunkSize - 1) / existingState.ChunkSize)
		completedSet := make(map[int]*OShinChunk)
		for i := range existingState.Chunks {
			completedSet[existingState.Chunks[i].ID] = &existingState.Chunks[i]
		}

		task.Chunks = make([]*types.ChunkInfo, chunkCount)
		var totalDownloaded int64
		for i := 0; i < chunkCount; i++ {
			// 默认按 ChunkSize 计算范围
			start := int64(i) * existingState.ChunkSize
			end := start + existingState.ChunkSize - 1
			if end >= existingState.TotalSize {
				end = existingState.TotalSize - 1
			}

			chunkStatus := types.ChunkStatusPending

			if cs, ok := completedSet[i]; ok {
				chunkStatus = types.ChunkStatusCompleted
				if cs.Start > 0 {
					start = cs.Start
				}
				if cs.End > 0 {
					end = cs.End
				}
				totalDownloaded += end - start + 1
			}

			task.Chunks[i] = &types.ChunkInfo{
				Index:  i,
				Start:  start,
				End:    end,
				Status: chunkStatus,
			}
		}
		if len(existingState.URLs) > 1 {
			task.Config.MultiSources = existingState.URLs[1:]
		}
		if existingState.ET != "" {
			parts := strings.SplitN(existingState.ET, ":", 2)
			if len(parts) == 2 {
				task.Config.ChecksumType = parts[0]
				task.Config.ChecksumValue = parts[1]
			}
		}
		// 将已恢复分片的字节数加入进度，避免进度从 0 开始
		if totalDownloaded > 0 {
			task.Progress.AddDownloaded(totalDownloaded)
		}
		resumedFromState = true
	} else {
		if err := d.initChunks(task); err != nil {
			return fmt.Errorf("failed to init chunks: %w", err)
		}
	}

	// resume 阶段结束，进入下载
	task.SetStatus(types.TaskStatusDownloading)

	// 计算实际并发数：min(分片数, 最大连接数)
	chunkCount := len(task.Chunks)
	effectiveConcurrency := task.Config.MaxConnections
	if chunkCount < effectiveConcurrency {
		effectiveConcurrency = chunkCount
	}
	if effectiveConcurrency < 1 {
		effectiveConcurrency = 1
	}

	// 打开或创建临时文件（支持续传）
	var tempFile *os.File
	var err error
	if resumedFromState {
		tempFile, err = os.OpenFile(tempPath, os.O_RDWR, 0644)
		if err != nil {
			resumedFromState = false
			tempFile, err = os.Create(tempPath)
		}
	} else {
		tempFile, err = os.Create(tempPath)
	}
	if err != nil {
		return fmt.Errorf("failed to open temp file: %w", err)
	}
	defer func() {
		tempFile.Close()
	}()

	// 预分配文件空间（如果支持）
	if task.Metadata.Size > 0 {
		fi, statErr := tempFile.Stat()
		if statErr == nil && fi.Size() >= task.Metadata.Size {
		} else {
			if err := tempFile.Truncate(task.Metadata.Size); err != nil {
				return fmt.Errorf("failed to allocate file space: %w", err)
			}
		}
	}

	// 创建取消上下文（用于外部取消，如 Ctrl+C / 暂停）
	// 普通分片失败不取消 context，让其他 worker 继续工作
	downloadCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// 构建加权 URL 列表（主 URL 占比高于其他 URL）
	d.weightedURLs = buildWeightedURLs(task.URL, task.Config.MultiSources)
	d.urlIndex = 0

	// 初始化进度跟踪
	// 注意：不设置 ActiveThreads 初始值，由 goroutine 的 Inc/Dec 自然管理
	// 避免 SetActiveThreads(N) + N 个 goroutine 各 IncActiveThreads() 导致计数翻倍
	task.Progress.SetRemainingChunks(int32(chunkCount))

	// 创建状态保存器（每 5 秒自动保存，分片完成时立即保存）
	stateSaver := NewStateSaver(task, oshinPath, 5*time.Second)
	stateSaver.Start()
	defer stateSaver.Stop()

	// 分片调度参数
	const (
		requeueLimit     = 2               // 单分片队尾重排上限（超过后标记 Failed，等待末尾补偿）
		cooldownFirst    = 2 * time.Second // 线程首次连续失败 cooldown
		cooldownSecond   = 8 * time.Second // 线程第二次连续失败 cooldown
		staggerDelay     = 300 * time.Millisecond // worker 错峰启动间隔
	)

	// 共享 FIFO 队列：worker 按顺序领取，失败分片（未超上限）append 队尾重新消费
	queue := make([]int, 0, len(task.Chunks))
	for _, chunk := range task.Chunks {
		if chunk.Status != types.ChunkStatusCompleted {
			queue = append(queue, chunk.Index)
		}
	}
	var head int // 已消费到的位置，避免频繁重建 slice

	var mu sync.Mutex
	var wg sync.WaitGroup
	rangeUnsupported := atomic.Bool{} // 任一 worker 遇到 Range 不支持时置位
	threadLimited := atomic.Int32{}   // 因连续失败退役的线程数

	for w := 0; w < effectiveConcurrency; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			task.Progress.IncActiveThreads()
			defer task.Progress.DecActiveThreads()

			// 错峰启动：失败窗口按 worker 序号天然打散，避免整批同时撞连接上限
			select {
			case <-time.After(time.Duration(workerID) * staggerDelay):
			case <-downloadCtx.Done():
				return
			}

			consecutiveErrs := 0 // 连续失败计数（成功清零），驱动 cooldown / 退役

			for {
				select {
				case <-downloadCtx.Done():
					return
				default:
				}

				mu.Lock()
				if head >= len(queue) {
					mu.Unlock()
					return
				}
				chunk := task.Chunks[queue[head]]
				head++
				mu.Unlock()

				if err := d.downloadChunk(downloadCtx, task, chunk, tempFile); err != nil {
					task.SetChunkError(chunk.Index, err)
					stateSaver.MarkDirty()

					errStr := err.Error()
					if strings.Contains(errStr, "ignored range request") || strings.Contains(errStr, "content-range misaligned") {
						// Range 不支持：同一 URL 所有分片都必然失败，立即降级单线程整文件下载
						rangeUnsupported.Store(true)
						cancel()
						return
					}

					// 先处置分片（回队尾或标 Failed）再进入 cooldown/退役，
					// 保证 cooldown 期间被中断时分片状态不丢失
					consecutiveErrs++
					if chunk.RetryCount < requeueLimit {
						task.IncChunkRetry(chunk.Index)
						task.UpdateChunkStatus(chunk.Index, types.ChunkStatusPending)
						mu.Lock()
						queue = append(queue, chunk.Index)
						mu.Unlock()
					} else {
						task.UpdateChunkStatus(chunk.Index, types.ChunkStatusFailed)
					}

					switch {
					case consecutiveErrs >= 3:
						// 第三次连续失败：视为线程受限，本次任务中不再重启该线程
						threadLimited.Add(1)
						return
					case consecutiveErrs == 2:
						select {
						case <-time.After(cooldownSecond):
						case <-downloadCtx.Done():
							return
						}
					default:
						select {
						case <-time.After(cooldownFirst):
						case <-downloadCtx.Done():
							return
						}
					}
					continue
				}

				// 成功：清空连续失败计数
				consecutiveErrs = 0
				task.SetChunkError(chunk.Index, nil)
				stateSaver.MarkDirty()
				stateSaver.Save()
				mu.Lock()
				remaining := int32(len(queue) - head)
				mu.Unlock()
				task.Progress.SetRemainingChunks(remaining)
			}
		}(w)
	}

	// 等待所有 worker 完成（带强制超时，防止 Ctrl+C 后长时间阻塞）
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		// 强制退出：关闭所有空闲连接，打断阻塞在 body.Read() 上的 worker
		d.transport.CloseIdleConnections()
		<-done
	}

	// Range 不支持：降级为单线程不分片整文件下载（已下载分片数据作废）
	// 使用原始 ctx：降级下载仍响应暂停/Ctrl+C
	if rangeUnsupported.Load() {
		if dlErr := d.downloadSingleStream(ctx, task, tempFile); dlErr != nil {
			// 旧 .oshin 记录的是分片下载状态，对整文件下载无意义，清理避免误导 resume
			RemoveOShinState(oshinPath)
			// Fail 内部跳过 PAUSED（用户主动暂停优先于失败）
			task.Fail(dlErr)
			return dlErr
		}
		stateSaver.Stop()
		// 下载完成，重命名临时文件并清理状态
		tempFile.Close()
		if err := os.Rename(tempPath, outputPath); err != nil {
			return fmt.Errorf("failed to rename temp file: %w", err)
		}
		RemoveOShinState(oshinPath)
		task.SetStatus(types.TaskStatusCompleted)
		return nil
	}

	// 常规消费结束后统一收口：所有未完成（Pending/DOWNLOADING/Failed）的分片
	// 一律标记为 Failed 进入末尾补偿。
	// 覆盖两类滞留：① 线程全部退役时已回队尾但未被消费的 Pending 分片；
	// ② cooldown 中断时停留在 DOWNLOADING 的分片。
	// 若不放任其绕过失败统计，最终会 rename 出带洞文件
	for _, chunk := range task.Chunks {
		if chunk.Status != types.ChunkStatusCompleted {
			task.UpdateChunkStatus(chunk.Index, types.ChunkStatusFailed)
		}
	}

	// 末尾补偿消费：常规消费结束后（队列空或线程全部退役），串行重试所有
	// Failed 分片各一次。此时无并发争抢、服务器连接压力最小，最大化文件完整性
	// （补偿期间用户中断经 ctx 传递，直接跳到中断处理）
	compensate := func() {
		for _, chunk := range task.Chunks {
			if downloadCtx.Err() != nil {
				return
			}
			if chunk.Status != types.ChunkStatusFailed {
				continue
			}
			task.UpdateChunkStatus(chunk.Index, types.ChunkStatusPending)
			if err := d.downloadChunk(downloadCtx, task, chunk, tempFile); err != nil {
				task.SetChunkError(chunk.Index, err)
				task.UpdateChunkStatus(chunk.Index, types.ChunkStatusFailed)
				stateSaver.MarkDirty()
				continue
			}
			task.SetChunkError(chunk.Index, nil)
			stateSaver.MarkDirty()
			stateSaver.Save()
		}
	}
	compensate()

	stateSaver.Stop()

	// 检查是否被中断（Ctrl+C / 暂停），中断时保留 .oshin 状态文件用于续传
	// Fail 内部跳过 PAUSED（用户主动暂停优先于失败）
	if downloadCtx.Err() != nil {
		task.Fail(downloadCtx.Err())
		return downloadCtx.Err()
	}

	// 所有线程退役且仍有未完成分片：线程受限，任务失败
	// 存在失败分片时禁止重命名，避免产出带空洞的不完整文件
	// （.oshin 断点状态已保存，可通过 resume 恢复）
	// 此时所有 worker 已结束，直接读 chunk 字段无并发风险
	var errDetails []string
	finalFailed := 0
	for _, chunk := range task.Chunks {
		if chunk.Status != types.ChunkStatusFailed {
			continue
		}
		finalFailed++
		if chunk.Error != nil && len(errDetails) < 3 {
			errDetails = append(errDetails, fmt.Sprintf("chunk %d: %v", chunk.Index, chunk.Error))
		}
	}
	task.Progress.SetFailedChunks(int32(finalFailed))

	if finalFailed > 0 {
		detail := ""
		if len(errDetails) > 0 {
			detail = ": " + strings.Join(errDetails, "; ")
		}
		msg := fmt.Sprintf("%d of %d chunks failed%s", finalFailed, len(task.Chunks), detail)
		if int(threadLimited.Load()) >= effectiveConcurrency && finalFailed+task.GetCompletedChunkCount() < len(task.Chunks) {
			msg = fmt.Sprintf("thread limit reached: %s", msg)
		}
		chunkErr := errors.New(msg)
		task.Fail(chunkErr)
		return chunkErr
	}

	// 下载完成，重命名临时文件
	tempFile.Close()
	if err := os.Rename(tempPath, outputPath); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	// 清理 .oshin 状态文件
	RemoveOShinState(oshinPath)

	task.SetStatus(types.TaskStatusCompleted)
	return nil
}

// initChunks 初始化分片信息
func (d *HTTPDownloader) initChunks(task *types.DownloadTask) error {
	if task.Metadata.Size <= 0 {
		metadata, err := Probe(task.URL, task.Config)
		if err != nil {
			return fmt.Errorf("failed to probe file: %w", err)
		}
		task.Metadata = metadata
	}

	// 如果文件大小仍未知，使用单片下载
	if task.Metadata.Size <= 0 {
		task.Chunks = []*types.ChunkInfo{
			{
				Index:  0,
				Start:  0,
				End:    -1, // 表示下载到末尾
				Status: types.ChunkStatusPending,
			},
		}
		return nil
	}

	// 如果不支持断点续传或文件小于分片大小，使用单片
	if !task.Metadata.SupportResume || task.Metadata.Size < task.Config.ChunkSize {
		task.Chunks = []*types.ChunkInfo{
			{
				Index:  0,
				Start:  0,
				End:    task.Metadata.Size - 1,
				Status: types.ChunkStatusPending,
			},
		}
		return nil
	}

	// 计算分片数量
	chunkCount := int((task.Metadata.Size + task.Config.ChunkSize - 1) / task.Config.ChunkSize)
	task.Chunks = make([]*types.ChunkInfo, chunkCount)

	if len(task.Config.MultiSources) > 0 {
		task.MultiSource = true
	}

	// 初始化分片信息（不再分配 SourceURL，下载时统一使用 task.URL）
	for i := 0; i < chunkCount; i++ {
		start := int64(i) * task.Config.ChunkSize
		end := start + task.Config.ChunkSize - 1
		if end >= task.Metadata.Size {
			end = task.Metadata.Size - 1
		}

		task.Chunks[i] = &types.ChunkInfo{
			Index:  i,
			Start:  start,
			End:    end,
			Status: types.ChunkStatusPending,
		}
	}

	return nil
}

// downloadChunk 下载单个分片
func (d *HTTPDownloader) downloadChunk(ctx context.Context, task *types.DownloadTask, chunk *types.ChunkInfo, tempFile *os.File) error {
	task.UpdateChunkStatus(chunk.Index, types.ChunkStatusDownloading)

	downloadURL := d.nextURL()
	if downloadURL == "" {
		downloadURL = task.URL
	}

	// 重试逻辑
	var lastErr error
	for retry := 0; retry <= task.Config.Retry; retry++ {
		select {
		case <-ctx.Done():
			task.UpdateChunkStatus(chunk.Index, types.ChunkStatusFailed)
			return ctx.Err()
		default:
		}

		if retry > 0 {
			delay := task.Config.RetryDelay * time.Duration(retry)
			if delay > 5*time.Second {
				delay = 5 * time.Second
			}
			time.Sleep(delay)
		}

		// 创建 HTTP 请求
		req, err := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
		if err != nil {
			lastErr = fmt.Errorf("failed to create request: %w", err)
			continue
		}

		// 设置 Range 请求头（支持断点续传）
		startPos := chunk.Start + chunk.LocalOffset
		if chunk.End >= 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", startPos, chunk.End))
		} else {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", startPos))
		}

		req.Header.Set("User-Agent", "OShinD/1.0")
		req.Header.Set("Content-Type", detectContentType(task.FileName))
		applyHeaders(req, task.Config.Headers)

		chunk.Headers = make(map[string]string)
		for key := range req.Header {
			chunk.Headers[key] = req.Header.Get(key)
		}

		client := d.getAdaptiveClient(downloadURL, retry > 0)

		connStart := time.Now()

		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("HTTP request failed: %w", err)
			continue
		}

		// 检查响应状态码
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			resp.Body.Close()
			if resp.StatusCode == http.StatusForbidden {
				task.UpdateChunkStatus(chunk.Index, types.ChunkStatusFailed)
				return fmt.Errorf("forbidden: server connection limit")
			}
			lastErr = fmt.Errorf("unexpected status code: %d", resp.StatusCode)
			continue
		}

		// 服务器忽略 Range 返回 200 且请求起点非 0 时，数据无法对齐写入，
		// 继续重试无意义，直接失败（错误信息经任务 error 字段透出）
		if resp.StatusCode == http.StatusOK && startPos > 0 {
			resp.Body.Close()
			task.UpdateChunkStatus(chunk.Index, types.ChunkStatusFailed)
			return fmt.Errorf("server ignored range request at offset %d", startPos)
		}

		// 206 校验 Content-Range 起始偏移：防止中间层错位返回导致写入错误位置
		if resp.StatusCode == http.StatusPartialContent && startPos > 0 {
			if cr := resp.Header.Get("Content-Range"); cr != "" {
				// 格式 bytes S-E/T
				if dash := strings.IndexByte(cr, '-'); dash > 0 {
					seg := cr[:dash]
					if sp := strings.LastIndexByte(seg, ' '); sp >= 0 {
						seg = seg[sp+1:]
					}
					if s, perr := strconv.ParseInt(strings.TrimSpace(seg), 10, 64); perr == nil && s != startPos {
						resp.Body.Close()
						lastErr = fmt.Errorf("content-range misaligned: got start %d, want %d", s, startPos)
						continue
					}
				}
			}
		}

		// 206 响应限定读取跨度：服务器提前 EOF 或超额发送都不会静默破坏分片完整性
		body := io.Reader(resp.Body)
		if resp.StatusCode == http.StatusPartialContent && chunk.End >= 0 {
			if remaining := chunk.End - (chunk.Start + chunk.LocalOffset) + 1; remaining > 0 {
				body = io.LimitReader(resp.Body, remaining)
			}
		}

		// 下载数据
		err = d.readChunkData(ctx, task, chunk, body, tempFile)
		resp.Body.Close()

		if err == nil {
			connDuration := time.Since(connStart)
			d.updateMaxConnTime(connDuration)
			return nil
		}
		lastErr = err
	}

	// 所有重试都失败
	task.UpdateChunkStatus(chunk.Index, types.ChunkStatusFailed)
	return fmt.Errorf("chunk %d failed after %d retries: %w", chunk.Index, task.Config.Retry, lastErr)
}

// readChunkData 读取分片数据并写入文件
func (d *HTTPDownloader) readChunkData(ctx context.Context, task *types.DownloadTask, chunk *types.ChunkInfo, body io.Reader, tempFile *os.File) error {
	buf := make([]byte, 32*1024) // 32KB 缓冲区
	offset := chunk.Start + chunk.LocalOffset

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, readErr := body.Read(buf)
		if n > 0 {
			_, writeErr := tempFile.WriteAt(buf[:n], offset)
			if writeErr != nil {
				return fmt.Errorf("failed to write chunk: %w", writeErr)
			}
			offset += int64(n)
			chunk.LocalOffset += int64(n)
			chunk.Downloaded += int64(n)
			task.Progress.AddDownloaded(int64(n))
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return fmt.Errorf("failed to read chunk: %w", readErr)
		}
	}

	// 分片完整性校验：EOF 时已收字节数必须达到分片跨度（跨重试累计）。
	// 防止 CDN 单请求配额截断/连接中断等产生的截断响应被静默标记完成，
	// 产出带空洞的文件；不足部分经 downloadChunk 重试从断点续传
	if chunk.End >= 0 {
		if span := chunk.End - chunk.Start + 1; chunk.Downloaded < span {
			task.UpdateChunkStatus(chunk.Index, types.ChunkStatusFailed)
			return fmt.Errorf("chunk %d truncated: got %d of %d bytes", chunk.Index, chunk.Downloaded, span)
		}
	}

	// 更新分片状态
	task.UpdateChunkStatus(chunk.Index, types.ChunkStatusCompleted)
	return nil
}

// getOutputPath 获取输出文件路径
func (d *HTTPDownloader) getOutputPath(task *types.DownloadTask) string {
	if task.FileName != "" {
		return filepath.Join(task.Config.OutputDir, task.FileName)
	}

	// 从 URL 提取文件名
	fileName := ExtractFileInfo(task.URL)
	return filepath.Join(task.Config.OutputDir, fileName)
}

// buildFastFailClient 构建快速失败客户端
// 调用方须持有 d.mu 锁
func (d *HTTPDownloader) buildFastFailClient() *http.Client {
	timeout := 3 * time.Second
	if d.maxConnTime > timeout {
		timeout = d.maxConnTime
	}

	transport := &http.Transport{}
	if d.tlsConfig != nil {
		transport.TLSClientConfig = d.tlsConfig
	}

	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
}

// updateMaxConnTime 更新最大成功连接时间（线程安全）
func (d *HTTPDownloader) updateMaxConnTime(duration time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if duration > d.maxConnTime {
		d.maxConnTime = duration
		if d.fastFailClient != nil {
			d.fastFailClient.CloseIdleConnections()
		}
		d.fastFailClient = d.buildFastFailClient()
	}
}

// getAdaptiveClient 获取自适应客户端（线程安全）
func (d *HTTPDownloader) getAdaptiveClient(downloadURL string, isRetry bool) *http.Client {
	if isRetry {
		d.mu.Lock()
		ffc := d.fastFailClient
		d.mu.Unlock()
		if ffc != nil {
			return ffc
		}
	}

	// 否则使用普通客户端
	client := d.client
	if c, ok := d.clients[downloadURL]; ok {
		client = c
	}
	return client
}

// mimeTypes 文件扩展名 → MIME 类型映射
var mimeTypes = map[string]string{
	".apk":  "application/vnd.android.package-archive",
	".zip":  "application/zip",
	".rar":  "application/x-rar-compressed",
	".7z":   "application/x-7z-compressed",
	".tar":  "application/x-tar",
	".gz":   "application/gzip",
	".exe":  "application/x-msdownload",
	".msi":  "application/x-msdownload",
	".dmg":  "application/x-apple-diskimage",
	".iso":  "application/x-iso9660-image",
	".pdf":  "application/pdf",
	".mp4":  "video/mp4",
	".mkv":  "video/x-matroska",
	".avi":  "video/x-msvideo",
	".mp3":  "audio/mpeg",
	".flac": "audio/flac",
	".wav":  "audio/wav",
	".ogg":  "audio/ogg",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
	".svg":  "image/svg+xml",
	".html": "text/html",
	".css":  "text/css",
	".js":   "application/javascript",
	".json": "application/json",
	".xml":  "application/xml",
	".txt":  "text/plain",
	".doc":  "application/msword",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xls":  "application/vnd.ms-excel",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".ppt":  "application/vnd.ms-powerpoint",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
}

// detectContentType 根据文件扩展名推断 Content-Type
func detectContentType(filename string) string {
	ext := strings.ToLower(filename)
	if dotIdx := strings.LastIndex(ext, "."); dotIdx >= 0 {
		ext = ext[dotIdx:]
		if ct, ok := mimeTypes[ext]; ok {
			return ct
		}
	}
	return "application/octet-stream"
}
