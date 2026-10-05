package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mogumc/oshind/pkg/downloader"
	"github.com/mogumc/oshind/types"
)

// ── bubbletea messages ──

// linesMsg ProgressReporter 通过 channel 发送的进度行
type linesMsg struct {
	lines []string
}

// statusMsg 任务状态变化消息
type statusMsg struct {
	status types.TaskStatus
}

// doneMsg 下载完成信号（携带 task 结果）
type doneMsg struct {
	task *types.DownloadTask
	err  error
}

// ── progressModel ──

// progressModel bubbletea model，接收 ProgressReporter 的回调行并渲染
type progressModel struct {
	lines       []string         // 当前帧的进度行
	status      types.TaskStatus // 当前任务状态
	result      *types.DownloadTask
	linesChan   chan []string
	statusChan  chan types.TaskStatus
	doneChan    chan *doneMsg
	done        bool
	err         error
	quitting    bool
	interrupted bool // Ctrl+C 触发的暂停中断
}

// newProgressModel 创建 progressModel 并返回 channels 供外部注入回调
func newProgressModel() (progressModel, chan []string, chan types.TaskStatus, chan *doneMsg) {
	linesChan := make(chan []string, 16)
	statusChan := make(chan types.TaskStatus, 8)
	doneChan := make(chan *doneMsg, 1)
	return progressModel{
		linesChan:  linesChan,
		statusChan: statusChan,
		doneChan:   doneChan,
	}, linesChan, statusChan, doneChan
}

func (m progressModel) Init() tea.Cmd {
	return waitForUpdate(m.linesChan, m.statusChan, m.doneChan)
}

func (m progressModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case linesMsg:
		m.lines = msg.lines
		return m, waitForUpdate(m.linesChan, m.statusChan, m.doneChan)
	case statusMsg:
		m.status = msg.status
		return m, waitForUpdate(m.linesChan, m.statusChan, m.doneChan)
	case doneMsg:
		m.done = true
		m.err = msg.err
		m.result = msg.task
		// 更新为任务最终状态，确保 View() 渲染正确的完成/失败信息
		if msg.task != nil {
			m.status = msg.task.GetStatus()
		}
		return m, tea.Quit
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.interrupted = true
			m.quitting = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m progressModel) View() string {
	if m.quitting {
		// Ctrl+C 中断时保留最后的进度帧，避免 Bubbletea 退出时清屏导致后续摘要无输出
		if m.interrupted {
			var parts []string
			if m.status != 0 {
				parts = append(parts, fmt.Sprintf("  %s: %s", T("状态", "Status"), m.status.String()))
			}
			if len(m.lines) > 0 {
				parts = append(parts, m.lines...)
			}
			parts = append(parts, "  ⏸️ "+statusPaused)
			return "\n" + strings.Join(parts, "\n") + "\n"
		}
		return ""
	}
	if len(m.lines) == 0 && m.status == 0 {
		return ""
	}

	var statusLine string
	if m.status != 0 {
		switch m.status {
		case types.TaskStatusPending:
			statusLine = "  ⏳ " + statusPending
		case types.TaskStatusProbing:
			statusLine = "  🔍 " + statusProbing
		case types.TaskStatusDownloading:
			statusLine = "  ⚡️ " + statusDownloading
		case types.TaskStatusVerifying:
			statusLine = "  ✅ " + statusVerifying
		case types.TaskStatusCompleted:
			statusLine = "  ✅ " + statusCompleted
		case types.TaskStatusFailed:
			statusLine = "  ❌ " + statusFailed
		case types.TaskStatusPaused:
			statusLine = "  ⏸️ " + statusPaused
		case types.TaskStatusResuming:
			statusLine = "  🔄 " + statusResuming
		case types.TaskStatusCancelled:
			statusLine = "  🚫 " + statusCancelled
		default:
			statusLine = fmt.Sprintf("  %s: %s", T("状态", "Status"), m.status.String())
		}
	}

	var parts []string
	if statusLine != "" {
		parts = append(parts, statusLine)
	}
	if len(m.lines) > 0 {
		parts = append(parts, m.lines...)
	}

	return "\n" + strings.Join(parts, "\n") + "\n"
}

// waitForUpdate 返回一个 tea.Cmd，阻塞直到 linesChan、statusChan 或 doneChan 有数据
// 优先非阻塞检查 statusChan，避免空 lines 消息抢占导致状态显示延迟
func waitForUpdate(linesChan chan []string, statusChan chan types.TaskStatus, doneChan chan *doneMsg) tea.Cmd {
	return func() tea.Msg {
		// 优先级：status > done > lines
		// 非阻塞检查 status，确保状态变化不被 linesChan 的空消息延迟
		select {
		case status := <-statusChan:
			return statusMsg{status: status}
		default:
		}
		// 阻塞等待任意 channel
		select {
		case status := <-statusChan:
			return statusMsg{status: status}
		case msg := <-doneChan:
			return doneMsg{task: msg.task, err: msg.err}
		case lines := <-linesChan:
			return linesMsg{lines: lines}
		}
	}
}

// ── TTY 检测 ──

// isInteractive 检测 stdout 是否为交互终端（非管道/重定向）
func isInteractive() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// ── 非 TTY 进度回调 ──

// simpleOnReport 非终端模式：直接滚动输出进度行
func simpleOnReport(lines []string) {
	for _, line := range lines {
		fmt.Println(line)
	}
}

// ── 格式化工具 ──

// formatBytes 格式化字节大小
func formatBytes(bytes int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)
	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.2f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.2f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// ── 完成信息渲染 ──

// printSuccess 输出成功信息
func printSuccess(fileName string, size int64) {
	if isInteractive() {
		msg := fmt.Sprintf("%s: %s", sumSaved, fileName)
		if size > 0 {
			msg = fmt.Sprintf("%s: %s (%s)", sumSaved, fileName, formatBytes(size))
		}
		fmt.Println("\n" + SuccessStyle.Render("✓ ") + SavedStyle.Render(msg))
	} else {
		if size > 0 {
			fmt.Printf("%s: %s (%s)\n", sumSaved, fileName, formatBytes(size))
		} else {
			fmt.Printf("%s: %s\n", sumSaved, fileName)
		}
	}
}

// printDownloadSummary 输出下载完成后的汇总信息，包含 probe 信息和验证结果
func printDownloadSummary(task *types.DownloadTask) {
	if task == nil {
		return
	}

	// 先输出保存信息
	var fileSize int64
	if task.Metadata != nil {
		fileSize = task.Metadata.Size
	}
	printSuccess(task.FileName, fileSize)

	if isInteractive() {
		fmt.Println()
		fmt.Println(renderDivider(sumTitle))
		fmt.Println()

		// probe 信息
		fmt.Println(renderKV(sumURL, task.URL))
		if task.Metadata != nil {
			if task.Metadata.FileName != "" {
				fmt.Println(renderKV(sumFile, task.Metadata.FileName))
			}
			if task.Metadata.Size > 0 {
				fmt.Println(renderKV(sumSize, formatBytes(task.Metadata.Size)))
			}
			if task.Metadata.ContentType != "" {
				fmt.Println(renderKV(sumType, task.Metadata.ContentType))
			}
			fmt.Println(renderKV(sumResume, fmt.Sprintf("%v", task.Metadata.SupportResume)))
			if task.Metadata.Checksum != "" {
				if task.Metadata.ChecksumType != "" {
					fmt.Println(renderKV(sumChecksum, fmt.Sprintf("%s:%s", task.Metadata.ChecksumType, task.Metadata.Checksum)))
				} else {
					fmt.Println(renderKV(sumChecksum, task.Metadata.Checksum))
				}
			}
		}
		fmt.Println(renderKV(sumProtocol, task.Protocol.String()))

		// 验证信息
		if task.Verify != nil {
			fmt.Println()
			fmt.Println(renderDivider(T("验证", "Verification")))
			fmt.Println()
			if task.Verify.Skipped {
				fmt.Println(renderKV(T("状态", "Status"), verifySkipped))
				fmt.Println(renderKV(T("原因", "Reason"), verifyNoChecksum))
			} else {
				fmt.Println(renderKV(verifyMethod, task.Verify.Method))
				fmt.Println(renderKV(verifyExpected, task.Verify.Expected))
				fmt.Println(renderKV(verifyActual, task.Verify.Actual))
				if task.Verify.Passed {
					fmt.Println(renderKV(verifyResult, SuccessStyle.Render(verifyPassed)))
				} else {
					fmt.Println(renderKV(verifyResult, ErrorStyle.Render(verifyFailed)))
				}
			}
		}
		fmt.Println()
	} else {
		fmt.Printf("%s:\n", sumTitle)
		fmt.Printf("  %-10s%s\n", sumURL+":", task.URL)
		if task.Metadata != nil {
			if task.Metadata.FileName != "" {
				fmt.Printf("  %-10s%s\n", sumFile+":", task.Metadata.FileName)
			}
			if task.Metadata.Size > 0 {
				fmt.Printf("  %-10s%s\n", sumSize+":", formatBytes(task.Metadata.Size))
			}
			if task.Metadata.ContentType != "" {
				fmt.Printf("  %-10s%s\n", sumType+":", task.Metadata.ContentType)
			}
			fmt.Printf("  %-10s%v\n", sumResume+":", task.Metadata.SupportResume)
			if task.Metadata.Checksum != "" {
				fmt.Printf("  %-10s%s\n", sumChecksum+":", task.Metadata.Checksum)
			}
		}
		fmt.Printf("  %-10s%s\n", sumProtocol+":", task.Protocol.String())

		if task.Verify != nil {
			fmt.Printf("%s:\n", T("验证", "Verification"))
			if task.Verify.Skipped {
				fmt.Printf("  %-10s%s (%s)\n", T("状态", "Status")+":", verifySkipped, verifyNoChecksum)
			} else {
				fmt.Printf("  %-10s%s\n", verifyMethod+":", task.Verify.Method)
				fmt.Printf("  %-10s%s\n", verifyExpected+":", task.Verify.Expected)
				fmt.Printf("  %-10s%s\n", verifyActual+":", task.Verify.Actual)
				if task.Verify.Passed {
					fmt.Printf("  %-10s%s\n", verifyResult+":", verifyPassed)
				} else {
					fmt.Printf("  %-10s%s\n", verifyResult+":", verifyFailed)
				}
			}
		}
	}
}

// printError 输出错误信息（TTY 用 lipgloss，非 TTY 用 plain text）
func printError(msg string) {
	if isInteractive() {
		fmt.Fprintln(os.Stderr, ErrorStyle.Render("✗ ")+msg)
	} else {
		fmt.Fprintf(os.Stderr, "error: %s\n", msg)
	}
}

// printInfo 输出信息（TTY 用 lipgloss，非 TTY 用 plain text）
func printInfo(label, val string) {
	if isInteractive() {
		fmt.Println(InfoStyle.Render(label) + " " + val)
	} else {
		fmt.Printf("%s %s\n", label, val)
	}
}

// printInterruptSummary 输出 Ctrl+C 暂停时的摘要信息
func printInterruptSummary(task *types.DownloadTask, rawURL string, config *types.DownloadConfig) {
	if task == nil {
		fmt.Fprintln(os.Stderr, sumInterruptTip)
		return
	}

	downloaded := task.Progress.GetDownloaded()
	total := int64(0)
	if task.Metadata != nil {
		total = task.Metadata.Size
	}
	completedChunks := task.GetCompletedChunkCount()
	totalChunks := len(task.Chunks)

	// 构建状态文件路径
	statePath := ""
	if task.FileName != "" {
		statePath = downloader.GetOShinStatePath(filepath.Join(config.OutputDir, task.FileName))
	}

	if isInteractive() {
		fmt.Println()
		fmt.Println(WarningStyle.Render("  ⏸️ ") + WarningStyle.Render(statusPaused))
		fmt.Println()
		fmt.Println(renderDivider(T("暂停摘要", "Pause Summary")))
		fmt.Println()
		fmt.Println(renderKV(probeURL, rawURL))
		if task.FileName != "" {
			fmt.Println(renderKV(sumFile, task.FileName))
		}
		if total > 0 {
			fmt.Println(renderKV(sumSize, formatBytes(total)))
		}
		if downloaded > 0 {
			pct := ""
			if total > 0 {
				pct = fmt.Sprintf(" (%.1f%%)", float64(downloaded)/float64(total)*100)
			}
			fmt.Println(renderKV(progressDownloaded, formatBytes(downloaded)+pct))
		}
		if totalChunks > 0 {
			fmt.Println(renderKV(sumChunkProgress, fmt.Sprintf("%d/%d", completedChunks, totalChunks)))
		}
		if task.Metadata != nil && task.Metadata.Checksum != "" {
			if task.Metadata.ChecksumType != "" {
				fmt.Println(renderKV(sumChecksum, fmt.Sprintf("%s:%s", task.Metadata.ChecksumType, task.Metadata.Checksum)))
			} else {
				fmt.Println(renderKV(sumChecksum, task.Metadata.Checksum))
			}
		}
		fmt.Println(renderKV(sumProtocol, task.Protocol.String()))
		fmt.Println()
		if statePath != "" {
			fmt.Println(InfoStyle.Render("  ✓ ") + sumStateFile + ": " + statePath)
		} else {
			fmt.Println(InfoStyle.Render("  ✓ ") + T("续传状态已保存", "Resume state saved"))
		}
		fmt.Println(InfoStyle.Render("  ℹ ") + sumInterruptTip)
		fmt.Println()
	} else {
		fmt.Printf("\n%s\n\n", statusPaused)
		fmt.Printf("  %-10s%s\n", probeURL+":", rawURL)
		if task.FileName != "" {
			fmt.Printf("  %-10s%s\n", sumFile+":", task.FileName)
		}
		if total > 0 {
			fmt.Printf("  %-10s%s\n", sumSize+":", formatBytes(total))
		}
		if downloaded > 0 {
			pct := ""
			if total > 0 {
				pct = fmt.Sprintf(" (%.1f%%)", float64(downloaded)/float64(total)*100)
			}
			fmt.Printf("  %-10s%s\n", progressDownloaded+":", formatBytes(downloaded)+pct)
		}
		if totalChunks > 0 {
			fmt.Printf("  %-10s%d/%d\n", sumChunkProgress+":", completedChunks, totalChunks)
		}
		if task.Metadata != nil && task.Metadata.Checksum != "" {
			fmt.Printf("  %-10s%s\n", sumChecksum+":", task.Metadata.Checksum)
		}
		fmt.Printf("  %-10s%s\n", sumProtocol+":", task.Protocol.String())
		fmt.Println()
		if statePath != "" {
			fmt.Printf("  %s: %s\n", sumStateFile, statePath)
		} else {
			fmt.Printf("  %s\n", T("续传状态已保存", "Resume state saved"))
		}
		fmt.Printf("  %s\n", sumInterruptTip)
		fmt.Println()
	}
}

// renderDivider 渲染分隔线
func renderDivider(title string) string {
	if title == "" {
		return SeparatorStyle.Render(strings.Repeat("─", 48))
	}
	return HeaderStyle.Render(fmt.Sprintf("─ %s ─", title))
}

// ── CLI 侧进度报告器 ──

// ProgressReporter 从 task.Progress 读取下载状态，格式化输出进度行
// 完全运行在 CLI 侧，downloader 包不感知此结构
type ProgressReporter struct {
	task            *types.DownloadTask
	interval        time.Duration
	stopChan        chan struct{}
	statusChan      chan types.TaskStatus
	lastOutputLines int
	maxOutputLines  int
	started         bool
	stopOnce        sync.Once
	frameCount      int
	lastStatus      types.TaskStatus
	OnReport        func(lines []string)
	OnStop          func(maxLines int)
}

// NewProgressReporter 创建 CLI 侧进度报告器
func NewProgressReporter(task *types.DownloadTask, statusChan chan types.TaskStatus) *ProgressReporter {
	return &ProgressReporter{
		task:       task,
		interval:   500 * time.Millisecond,
		stopChan:   make(chan struct{}),
		statusChan: statusChan,
	}
}

// Start 开始报告进度（立即输出一次，然后定时刷新）
func (r *ProgressReporter) Start() {
	// 快速状态监听：每 10ms 轮询一次 task.GetStatus()，确保快速状态变化（如 Probing）
	// 不会被 500ms 的进度刷新间隔错过
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				currentStatus := r.task.GetStatus()
				if currentStatus != r.lastStatus {
					r.lastStatus = currentStatus
					if r.statusChan != nil {
						select {
						case r.statusChan <- currentStatus:
						default:
						}
					}
				}
			case <-r.stopChan:
				return
			}
		}
	}()

	// 慢速进度刷新：每 500ms 生成进度行（进度条、线程、速度等）
	r.report()
	go func() {
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				r.report()
			case <-r.stopChan:
				return
			}
		}
	}()
}

// Stop 停止报告进度
func (r *ProgressReporter) Stop() {
	r.stopOnce.Do(func() {
		close(r.stopChan)
		if r.maxOutputLines > 0 && r.started && r.OnStop != nil {
			r.OnStop(r.maxOutputLines)
		}
	})
}

// report 生成当前进度帧并通过 OnReport 回调输出
// 状态变化检测已移至 Start() 中的快速监听 goroutine（10ms 轮询）
func (r *ProgressReporter) report() {
	totalChunks := len(r.task.Chunks)
	if totalChunks == 0 {
		return
	}

	completedChunks := r.task.GetCompletedChunkCount()
	downloaded := r.task.Progress.GetDownloaded()
	total := r.task.Metadata.Size

	speed := r.task.Progress.CalculateSpeed()

	var eta time.Duration
	if speed > 0 && total > 0 && downloaded < total {
		remaining := float64(total-downloaded) / speed
		eta = time.Duration(remaining * float64(time.Second))
	}

	spinner := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
	r.frameCount++
	spinChar := spinner[r.frameCount%len(spinner)]

	var lines []string

	// 第1行：spinner + 进度条 + 速度 + ETA
	progress := float64(completedChunks) / float64(totalChunks) * 100
	bar := buildProgressBar(progress, 40)
	if speed > 0 {
		lines = append(lines, fmt.Sprintf("  %c %s %5.1f%% | %s/s | ETA: %s",
			spinChar, bar, progress, formatBytes(int64(speed)), formatDuration(eta)))
	} else if downloaded > 0 {
		lines = append(lines, fmt.Sprintf("  %c %s %5.1f%% | %s %s",
			spinChar, bar, progress, formatBytes(downloaded), progressDownloaded))
	} else {
		lines = append(lines, fmt.Sprintf("  %c %s %5.1f%% | %s",
			spinChar, bar, progress, progressConnecting))
	}

	// 第2行：线程统计
	activeThreads := r.task.Progress.GetActiveThreads()
	remainingChunks := r.task.Progress.GetRemainingChunks()
	failedChunks := r.task.Progress.GetFailedChunks()
	lines = append(lines, fmt.Sprintf("  %s: %d/%d  |  %s: %d %s  |  %s: %d",
		progressThreads, activeThreads, r.task.Config.MaxConnections,
		progressRemaining, remainingChunks, progressChunks,
		progressFailed, failedChunks))

	// 第3行起：活跃线程详情
	activeChunks := r.task.GetActiveChunks()
	if len(activeChunks) > 0 {
		lines = append(lines, "  ── "+progressActive+" ──")
		for i, chunk := range activeChunks {
			chunkSize := chunk.End - chunk.Start + 1
			chunkProgress := 0.0
			if chunkSize > 0 {
				chunkProgress = float64(chunk.Downloaded) / float64(chunkSize) * 100
			}
			miniBar := buildMiniBar(chunkProgress, 20)
			lines = append(lines, fmt.Sprintf("  [T%d] Chunk#%-3d %s %5.1f%%",
				i, chunk.Index, miniBar, chunkProgress))
		}
	}

	r.lastOutputLines = len(lines)
	if len(lines) > r.maxOutputLines {
		r.maxOutputLines = len(lines)
	}
	r.started = true

	if r.OnReport != nil {
		r.OnReport(lines)
	}
}

// ── 进度条构建 ──

// buildProgressBar 构建主进度条
func buildProgressBar(progress float64, width int) string {
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}

	filled := int(progress / 100.0 * float64(width))
	if filled > width {
		filled = width
	}

	var bar strings.Builder
	bar.WriteByte('[')
	for i := 0; i < width; i++ {
		if i < filled {
			bar.WriteByte('=')
		} else if i == filled && filled < width {
			bar.WriteByte('>')
		} else {
			bar.WriteByte(' ')
		}
	}
	bar.WriteByte(']')
	return bar.String()
}

// buildMiniBar 构建小型分片进度条
func buildMiniBar(progress float64, width int) string {
	filled := int(progress / 100.0 * float64(width))
	if filled > width {
		filled = width
	}

	var bar strings.Builder
	bar.WriteByte('[')
	for i := 0; i < width; i++ {
		if i < filled {
			bar.WriteByte('#')
		} else {
			bar.WriteByte('.')
		}
	}
	bar.WriteByte(']')
	return bar.String()
}

// formatDuration 格式化持续时间为可读格式
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		minutes := int(d.Minutes())
		seconds := int(d.Seconds()) % 60
		return fmt.Sprintf("%dm%ds", minutes, seconds)
	}
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh%dm", hours, minutes)
}
