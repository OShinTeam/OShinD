package main

import (
	"golang.org/x/text/language"
)

var locale string

func init() {
	locale = detectLocale()
	initTranslations()
}

func detectLocale() string {
	lang, _ := language.Parse("zh-CN")
	base, _ := lang.Base()
	if base.String() == "zh" {
		return "zh"
	}
	return "en"
}

// T 根据当前 locale 返回对应的翻译字符串
func T(zh, en string) string {
	if locale == "zh" {
		return zh
	}
	return en
}

// ── 翻译变量（在 initTranslations 中赋值，确保 locale 已设置） ──

// Help
var (
	hCmdDownload  string
	hCmdProbe     string
	hCmdHasResume string
	hCmdClear     string
	hCmdVersion   string
	hCmdHelp      string

	hSecCommands string
	hSecOptions  string

	hOptOutput   string
	hOptConn     string
	hOptChunk    string
	hOptTimeout  string
	hOptRetry    string
	hOptHeader   string
	hOptMulti    string
	hOptUser     string
	hOptPass     string
	hOptFtpPort  string
	hOptSftpPort string
	hOptNoTLS    string
	hOptNoCheck  string
	hOptNoResume string
	hOptCkType   string
	hOptCkValue  string
	hOptCkBoth   string
)

// Errors
var (
	errURLRequired     string
	errDirFileRequired string
	errUnknownCommand  string
	errRunHelp         string
	errDownloadFailed  string
	errProbeFailed     string
	errClearFailed     string
	errTUIError        string
)

// Status
var (
	statusPending     string
	statusProbing     string
	statusDownloading string
	statusVerifying   string
	statusCompleted   string
	statusFailed      string
	statusPaused      string
	statusResuming    string
	statusCancelled   string
)

// Progress
var (
	progressConnecting string
	progressDownloaded string
	progressThreads    string
	progressRemaining  string
	progressFailed     string
	progressChunks     string
	progressActive     string
)

// Verification
var (
	verifySkipped    string
	verifyNoChecksum string
	verifyMethod     string
	verifyExpected   string
	verifyActual     string
	verifyResult     string
	verifyPassed     string
	verifyFailed     string
)

// Probe Result
var (
	probeLabel  string
	probeURL    string
	probeFile   string
	probeSize   string
	probeType   string
	probeResume string
	probeServer string
	probeSpeed  string
)

// Summary
var (
	sumSaved       string
	sumTitle       string
	sumURL         string
	sumFile        string
	sumSize        string
	sumType        string
	sumResume      string
	sumChecksum    string
	sumProtocol    string
	sumResumeState string
	sumChunks      string
	sumChunkSize   string
	sumNoResume    string
	sumResumeFound string
	sumCleared     string
	sumProbe       string
	sumDownload    string
)

// Resume table
var (
	resumeID    string
	resumeStart string
	resumeEnd   string
	resumeMore  string
)

// Interrupt / Pause
var (
	sumInterruptTip  string
	sumChunkProgress string
	sumStateFile     string
)

// initTranslations 在 locale 设置后调用，填充所有翻译变量
func initTranslations() {
	// Help
	hCmdDownload = T("开始下载", "Start a download")
	hCmdProbe = T("探测服务器信息", "Probe server info")
	hCmdHasResume = T("检查续传状态", "Check resume state")
	hCmdClear = T("清除续传状态", "Clear resume state")
	hCmdVersion = T("显示版本", "Show version")
	hCmdHelp = T("显示此帮助", "Show this help")

	hSecCommands = T("命令", "Commands")
	hSecOptions = T("选项", "Options")

	hOptOutput = T("输出目录（默认：.）", "Output directory (default: .)")
	hOptConn = T("最大连接数（默认：4）", "Max connections (default: 4)")
	hOptChunk = T("分片大小，如 8m、1m", "Chunk size, e.g. 8m, 1m")
	hOptTimeout = T("请求超时（默认：30s）", "Request timeout (default: 30s)")
	hOptRetry = T("重试次数（默认：3）", "Retry count (default: 3)")
	hOptHeader = T("自定义 HTTP 头", "Custom HTTP header")
	hOptMulti = T("附加下载源", "Additional download source")
	hOptUser = T("FTP/SFTP 用户名", "FTP/SFTP username")
	hOptPass = T("FTP/SFTP 密码", "FTP/SFTP password")
	hOptFtpPort = T("FTP 端口（默认：取自地址或 21）", "FTP port (default: from address or 21)")
	hOptSftpPort = T("SFTP 端口（默认：取自地址或 22）", "SFTP port (default: from address or 22)")
	hOptNoTLS = T("跳过 TLS 验证", "Skip TLS verification")
	hOptNoCheck = T("跳过校验和验证", "Skip checksum verification")
	hOptNoResume = T("禁用续传", "Disable resume support")
	hOptCkType = T("校验和算法（md5/sha1/sha256）", "Checksum algorithm (md5/sha1/sha256)")
	hOptCkValue = T("预期校验和值", "Expected checksum value")
	hOptCkBoth = T("校验和（格式：type:value）", "Checksum as type:value")

	// Errors
	errURLRequired = T("url 是必填参数", "url is required")
	errDirFileRequired = T("output_dir 和 file_name 是必填参数", "output_dir and file_name are required")
	errUnknownCommand = T("未知命令", "unknown command")
	errRunHelp = T("运行 'oshind help' 查看用法。", "Run 'oshind help' for usage.")
	errDownloadFailed = T("下载失败", "download failed")
	errProbeFailed = T("探测失败", "probe failed")
	errClearFailed = T("清除失败", "clear failed")
	errTUIError = T("界面错误", "tui error")

	// Status
	statusPending = T("等待中...", "Pending...")
	statusProbing = T("探测中...", "Probing server...")
	statusDownloading = T("下载中...", "Downloading...")
	statusVerifying = T("校验中...", "Verifying checksum...")
	statusCompleted = T("已完成", "Completed")
	statusFailed = T("失败", "Failed")
	statusPaused = T("已暂停", "Paused")
	statusResuming = T("恢复下载...", "Resuming...")
	statusCancelled = T("已取消", "Cancelled")

	// Progress
	progressConnecting = T("连接中...", "connecting...")
	progressDownloaded = T("已下载", "downloaded")
	progressThreads = T("线程", "Threads")
	progressRemaining = T("剩余", "Remaining")
	progressFailed = T("失败", "Failed")
	progressChunks = T("分片", "chunks")
	progressActive = T("下载线程", "Active Threads")

	// Verification
	verifySkipped = T("跳过", "Skipped")
	verifyNoChecksum = T("无可用校验和", "No checksum available")
	verifyMethod = T("方法", "Method")
	verifyExpected = T("预期", "Expected")
	verifyActual = T("实际", "Actual")
	verifyResult = T("结果", "Result")
	verifyPassed = T("通过", "PASSED")
	verifyFailed = T("失败", "FAILED")

	// Probe Result
	probeLabel = T("探测结果", "Probe Result")
	probeURL = T("URL", "URL")
	probeFile = T("文件", "File")
	probeSize = T("大小", "Size")
	probeType = T("类型", "Type")
	probeResume = T("支持续传", "Resume")
	probeServer = T("服务器", "Server")
	probeSpeed = T("速度", "Speed")

	// Summary
	sumSaved = T("已保存", "Saved")
	sumTitle = T("下载摘要", "Download Summary")
	sumURL = T("URL", "URL")
	sumFile = T("文件", "File")
	sumSize = T("大小", "Size")
	sumType = T("类型", "Type")
	sumResume = T("支持续传", "Resume")
	sumChecksum = T("校验和", "Checksum")
	sumProtocol = T("协议", "Protocol")
	sumResumeState = T("续传状态", "Resume State")
	sumChunks = T("分片数", "Chunks")
	sumChunkSize = T("分片大小", "ChunkSize")
	sumNoResume = T("无续传状态", "No resume state found")
	sumResumeFound = T("找到续传状态：", "Resume state found:")
	sumCleared = T("续传状态已清除", "Resume state cleared")
	sumProbe = T("正在探测", "Probing")
	sumDownload = T("正在下载", "Downloading")

	// Resume table
	resumeID = T("ID", "ID")
	resumeStart = T("起始", "Start")
	resumeEnd = T("结束", "End")
	resumeMore = T("... 还有 %d 个", "... (%d more)")

	// Interrupt / Pause
	sumInterruptTip = T("重新运行相同的下载命令继续下载", "Re-run the same command to resume")
	sumChunkProgress = T("分片", "Chunks")
	sumStateFile = T("状态文件", "State file")
}
