package downloader

import (
	"crypto/tls"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mogumc/oshind/types"
)

// applyHeaders 将自定义请求头应用到 HTTP 请求
// 特殊处理 Host 头：Go 的 http.Request.Host 字段控制实际发送的 Host 头，
func applyHeaders(req *http.Request, headers map[string]string) {
	for key, value := range headers {
		if strings.EqualFold(key, "host") {
			req.Host = value
		} else {
			req.Header.Set(key, value)
		}
	}
}

// Probe 通过 GET Range: bytes=0-0 获取服务器资源元信息和断点续传支持情况。

func Probe(rawURL string, config *types.DownloadConfig) (*types.FileMetadata, error) {
	if config == nil {
		config = types.DefaultConfig()
	}

	client := newProbeClient(config)

	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", "OShinD/1.0")

	applyHeaders(req, config.Headers)

	req.Header.Set("Range", "bytes=0-0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("probe request failed: %w", err)
	}
	// 读至多 1 字节后主动关闭，避免触发服务器的全量传输
	io.CopyN(io.Discard, resp.Body, 1)
	resp.Body.Close()

	metadata := &types.FileMetadata{
		Size:          -1,
		SupportResume: false,
	}

	// 记录经过跳转后的最终 URL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL := resp.Request.URL.String()
		if finalURL != rawURL {
			metadata.FinalURL = finalURL
		}
	}

	// 206 Partial Content：服务器支持 Range，从 Content-Range 头解析真实文件大小
	// 格式：Content-Range: bytes 0-0/总大小
	if resp.StatusCode == http.StatusPartialContent {
		metadata.SupportResume = true
		if cr := resp.Header.Get("Content-Range"); cr != "" {
			metadata.AcceptRange = "bytes"
			// 解析 "bytes 0-0/N" 中的 N
			if slashIdx := strings.LastIndex(cr, "/"); slashIdx >= 0 {
				totalStr := strings.TrimSpace(cr[slashIdx+1:])
				if totalStr != "*" {
					if size, parseErr := strconv.ParseInt(totalStr, 10, 64); parseErr == nil {
						metadata.Size = size
					}
				}
			}
		}
	}

	// 服务器不支持 Range（200 OK）：回退读 Content-Length
	if metadata.Size < 0 {
		if cl := resp.Header.Get("Content-Length"); cl != "" {
			if size, parseErr := strconv.ParseInt(cl, 10, 64); parseErr == nil {
				metadata.Size = size
			}
		}
	}

	// Accept-Ranges 头也可作为断点续传依据
	if ar := resp.Header.Get("Accept-Ranges"); ar != "" && ar != "none" {
		metadata.SupportResume = true
		metadata.AcceptRange = ar
	}

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		metadata.ContentType = ct
	}

	if csType, csValue := DetectChecksumFromHeaders(resp.Header); csValue != "" {
		metadata.ChecksumType = csType
		metadata.Checksum = csValue
	}

	// HTML 中转页/防盗链提示页（200 忽略 Range）的 Content-Length 是页面大小而非文件大小，
	// 若当作文件大小切分片会产出截断文件：大小置为未知，回退单片下载到 EOF。
	// 置于校验和检测之后，一并清除从页面头误提取的校验值
	if resp.StatusCode == http.StatusOK && strings.HasPrefix(strings.ToLower(metadata.ContentType), "text/html") {
		metadata.Size = -1
		metadata.SupportResume = false
		metadata.ChecksumType, metadata.Checksum = "", ""
	}

	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if name := ParseContentDisposition(cd); name != "" {
			metadata.FileName = name
		}
	}

	return metadata, nil
}

// ProbeFull 执行完整的下载前探测，返回详细的服务器信息和连接测试结果
func ProbeFull(rawURL string, config *types.DownloadConfig) (*ProbeResult, error) {
	if config == nil {
		config = types.DefaultConfig()
	}

	result := &ProbeResult{
		URL:       rawURL,
		Timestamp: time.Now(),
	}

	metadata, err := Probe(rawURL, config)
	if err != nil {
		result.Error = err.Error()
		return result, err
	}
	result.Metadata = metadata

	speed, err := probeConnectionSpeed(rawURL, config)
	if err != nil {
		result.SpeedTestError = err.Error()
	} else {
		result.EstimatedSpeed = speed
	}

	result.ServerInfo = parseServerInfo(rawURL)

	return result, nil
}

// ProbeResult 完整探测结果
type ProbeResult struct {
	URL            string              `json:"url"`
	Timestamp      time.Time           `json:"timestamp"`
	Metadata       *types.FileMetadata `json:"metadata"`
	EstimatedSpeed float64             `json:"estimated_speed"` // 估算速度（字节/秒）
	ServerInfo     *ServerInfo         `json:"server_info"`
	Error          string              `json:"error,omitempty"`
	SpeedTestError string              `json:"speed_test_error,omitempty"`
}

// ServerInfo 服务器信息
type ServerInfo struct {
	Host   string `json:"host"`
	Port   string `json:"port"`
	Scheme string `json:"scheme"`
	Path   string `json:"path"`
}

// parseServerInfo 解析 URL 中的服务器信息
func parseServerInfo(rawURL string) *ServerInfo {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}

	host := u.Hostname()
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}

	return &ServerInfo{
		Host:   host,
		Port:   port,
		Scheme: u.Scheme,
		Path:   u.Path,
	}
}

// probeConnectionSpeed 通过下载小块数据测试连接速度
func probeConnectionSpeed(rawURL string, config *types.DownloadConfig) (float64, error) {
	client := newProbeClient(config)

	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return 0, err
	}

	req.Header.Set("Range", "bytes=0-262143") // 256KB
	req.Header.Set("User-Agent", "OShinD/1.0")

	applyHeaders(req, config.Headers)

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	buf := make([]byte, 256*1024)
	n, _ := io.ReadFull(resp.Body, buf)

	elapsed := time.Since(start).Seconds()
	if elapsed <= 0 {
		return 0, nil
	}

	speed := float64(n) / elapsed
	return speed, nil
}

// DetectProtocol 从 URL scheme 自动推断下载协议
func DetectProtocol(rawURL string) (types.Protocol, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return 0, fmt.Errorf("invalid URL: %w", err)
	}

	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "http":
		return types.ProtocolHTTP, nil
	case "https":
		return types.ProtocolHTTPS, nil
	case "ftp":
		return types.ProtocolFTP, nil
	case "sftp":
		return types.ProtocolSFTP, nil
	default:
		return 0, fmt.Errorf("unsupported protocol: %s", scheme)
	}
}

// newProbeClient 创建带 TLS 配置的探测用 HTTP 客户端
func newProbeClient(config *types.DownloadConfig) *http.Client {
	transport := &http.Transport{
		ResponseHeaderTimeout: config.Timeout,
	}
	if config.TLSConfig != nil && config.TLSConfig.InsecureSkipVerify {
		transport.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true,
		}
	}
	return &http.Client{
		Timeout:   config.Timeout,
		Transport: transport,
	}
}

// ExtractFileInfo 从 URL path 中提取文件名，处理编码和默认值
func ExtractFileInfo(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "unknown"
	}

	path := u.Path
	if path == "" || path == "/" {
		return "unknown"
	}

	parts := strings.Split(path, "/")
	fileName := parts[len(parts)-1]

	decoded, err := url.QueryUnescape(fileName)
	if err != nil {
		return fileName
	}

	if decoded == "" {
		return "unknown"
	}

	return decoded
}

// ParseContentDisposition 从 Content-Disposition 头中提取文件名
// 支持两种格式：
//   - filename="file.zip"  (RFC 6266)
//   - filename*=UTF-8”file%20name.zip  (RFC 5987, 优先级更高)
//
// 优先级：filename* > filename（RFC 5987 规定）
func ParseContentDisposition(header string) string {
	if header == "" {
		return ""
	}

	_, params, err := mime.ParseMediaType(header)
	if err != nil {
		return parseContentDispositionFallback(header)
	}

	if fnStar, ok := params["filename*"]; ok {
		if name := decodeRFC5987(fnStar); name != "" {
			return sanitizeFileName(name)
		}
	}

	if fn, ok := params["filename"]; ok {
		if name := strings.TrimSpace(fn); name != "" {
			return sanitizeFileName(name)
		}
	}

	return ""
}

// parseContentDispositionFallback 在 mime.ParseMediaType 失败时手动提取文件名
func parseContentDispositionFallback(header string) string {
	lower := strings.ToLower(header)

	if idx := strings.Index(lower, "filename*="); idx >= 0 {
		value := header[idx+len("filename*="):]
		value = strings.Trim(value, "\" ")
		if name := decodeRFC5987(value); name != "" {
			return sanitizeFileName(name)
		}
	}

	if idx := strings.Index(lower, "filename="); idx >= 0 {
		value := header[idx+len("filename="):]
		value = strings.Trim(value, "\" ")
		if semiIdx := strings.Index(value, ";"); semiIdx >= 0 {
			value = value[:semiIdx]
		}
		value = strings.TrimSpace(value)
		if value != "" {
			return sanitizeFileName(value)
		}
	}

	return ""
}

// decodeRFC5987 解码 RFC 5987 编码的 filename* 值
// 格式: charset'language'value（language 可为空）
func decodeRFC5987(value string) string {
	parts := strings.SplitN(value, "'", 3)
	if len(parts) != 3 {
		return ""
	}
	decoded, err := url.PathUnescape(parts[2])
	if err != nil {
		return parts[2]
	}
	return decoded
}

// sanitizeFileName 清理文件名，移除路径分隔符和危险字符
func sanitizeFileName(name string) string {
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "\x00", "")
	return strings.TrimSpace(name)
}
