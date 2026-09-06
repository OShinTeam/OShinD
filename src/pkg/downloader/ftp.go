package downloader

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jlaffaye/ftp"
	"github.com/mogumc/oshind/types"
)

type FTPDownloader struct {
}

// NewFTPDownloader 创建新的 FTP 下载器
func NewFTPDownloader() *FTPDownloader {
	return &FTPDownloader{}
}

// Download 建立 FTP 连接并下载文件（支持断点续传）
func (d *FTPDownloader) Download(ctx context.Context, task *types.DownloadTask) error {
	host, port, path, err := d.parseFTPAddress(task.URL)
	if err != nil {
		return fmt.Errorf("invalid FTP address: %w", err)
	}

	ftpConfig := task.Config.FTPConfig
	if ftpConfig == nil {
		ftpConfig = &types.FTPConfig{Port: port}
	} else if ftpConfig.Port == 0 {
		ftpConfig.Port = port
	}

	addr := fmt.Sprintf("%s:%d", host, ftpConfig.Port)
	conn, err := ftp.Dial(addr, ftp.DialWithTimeout(task.Config.Timeout))
	if err != nil {
		return fmt.Errorf("FTP connection failed: %w", err)
	}
	defer conn.Quit()

	if ftpConfig.Username != "" {
		err = conn.Login(ftpConfig.Username, ftpConfig.Password)
		if err != nil {
			return fmt.Errorf("FTP login failed: %w", err)
		}
	} else {
		// 无凭据时尝试匿名登录
		err = conn.Login("anonymous", "guest@")
		if err != nil {
			// 匿名登录失败不致命，部分服务器允许无登录访问
			fmt.Printf("FTP anonymous login skipped: %v\n", err)
		}
	}

	size, err := conn.FileSize(path)
	if err != nil {
		// 如果无法获取大小，尝试其他方式
		size = -1
	}

	task.Metadata.Size = size
	task.Metadata.SupportResume = true // FTP 支持断点续传

	// 输出目录兜底校验：不存在则创建，避免创建输出文件时才失败
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
		// FTP 单流下载，只比较文件大小
		if !d.validateFTResumeFile(existingState, tempPath) {
			RemoveOShinState(oshinPath)
			os.Remove(tempPath)
			existingState = nil
		}
	}
	if existingState != nil && existingState.TotalSize == task.Metadata.Size {
		resumedFromState = true
		// 恢复校验信息
		if existingState.ET != "" {
			parts := strings.SplitN(existingState.ET, ":", 2)
			if len(parts) == 2 {
				task.Config.ChecksumType = parts[0]
				task.Config.ChecksumValue = parts[1]
			}
		}
		// 恢复 headers/protocol/connections
		if len(existingState.Headers) > 0 {
			task.Config.Headers = existingState.Headers
		}
	}

	// resume 阶段结束，进入下载
	task.SetStatus(types.TaskStatusDownloading)

	// 打开或创建临时文件
	var outputFile *os.File
	var resumeOffset int64
	if resumedFromState {
		outputFile, err = os.OpenFile(tempPath, os.O_RDWR, 0644)
		if err != nil {
			resumedFromState = false
			outputFile, err = os.Create(tempPath)
		} else {
			// 获取已下载的字节数作为续传偏移
			fi, statErr := outputFile.Stat()
			if statErr == nil {
				resumeOffset = fi.Size()
			}
		}
	} else {
		outputFile, err = os.Create(tempPath)
	}
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer func() {
		outputFile.Close()
		// 如果任务失败，删除临时文件和状态文件
		if task.GetStatus() != types.TaskStatusCompleted && task.GetStatus() != types.TaskStatusPaused {
			os.Remove(tempPath)
			RemoveOShinState(oshinPath)
		}
	}()

	// 创建状态保存器（每 5 秒自动保存）
	stateSaver := NewStateSaver(task, oshinPath, 5*time.Second)
	stateSaver.Start()
	defer stateSaver.Stop()

	// FTP 使用单流下载，通过 RetrFrom 支持断点续传
	var reader *ftp.Response
	if resumeOffset > 0 {
		reader, err = conn.RetrFrom(path, uint64(resumeOffset))
		if err != nil {
			return fmt.Errorf("FTP retrieve (resume) failed: %w", err)
		}
		// 设置已下载进度
		task.Progress.AddDownloaded(resumeOffset)
	} else {
		reader, err = conn.Retr(path)
		if err != nil {
			return fmt.Errorf("FTP retrieve failed: %w", err)
		}
	}
	defer reader.Close()

	buffer := make([]byte, 32*1024) // 32KB 缓冲区

	for {
		select {
		case <-ctx.Done():
			// 保存当前状态以便续传
			stateSaver.ForceSave()
			return ctx.Err()
		default:
		}

		n, readErr := reader.Read(buffer)
		if n > 0 {
			_, writeErr := outputFile.Write(buffer[:n])
			if writeErr != nil {
				return fmt.Errorf("failed to write data: %w", writeErr)
			}
			task.Progress.AddDownloaded(int64(n))
			stateSaver.MarkDirty()
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return fmt.Errorf("FTP read failed: %w", readErr)
		}
	}

	// 下载完成，重命名临时文件为最终文件
	outputFile.Close()
	if err := os.Rename(tempPath, outputPath); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	// 清理状态文件
	stateSaver.Stop()
	RemoveOShinState(oshinPath)

	task.SetStatus(types.TaskStatusCompleted)
	return nil
}

// validateFTResumeFile 校验 FTP 续传文件的有效性
func (d *FTPDownloader) validateFTResumeFile(state *OShinState, tempPath string) bool {
	fi, err := os.Stat(tempPath)
	if err != nil || fi == nil {
		return false
	}

	// 比较文件大小
	if fi.Size() == state.TotalSize {
		return true
	}

	// 如果 temp 文件大小 >= totalSize，说明已经下载完成
	if fi.Size() >= state.TotalSize && state.TotalSize > 0 {
		return true
	}

	// 所有校验都失败
	return false
}

// parseFTPAddress 从 URL 中提取主机、端口和路径
func (d *FTPDownloader) parseFTPAddress(rawURL string) (host string, port int, path string, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", 0, "", fmt.Errorf("invalid URL: %w", err)
	}

	host = u.Hostname()
	if host == "" {
		return "", 0, "", fmt.Errorf("missing host")
	}

	portStr := u.Port()
	if portStr != "" {
		port, err = strconv.Atoi(portStr)
		if err != nil {
			return "", 0, "", fmt.Errorf("invalid port: %w", err)
		}
	} else {
		port = 21 // FTP 默认端口
	}

	path = u.Path
	if path == "" || path == "/" {
		path = "/"
	}

	return host, port, path, nil
}

// getOutputPath 获取输出文件路径
func (d *FTPDownloader) getOutputPath(task *types.DownloadTask) string {
	if task.FileName != "" {
		return filepath.Join(task.Config.OutputDir, task.FileName)
	}

	fileName := ExtractFileInfo(task.URL)
	return filepath.Join(task.Config.OutputDir, fileName)
}

// ProbeFTP 测试 FTP 服务器的可达性
func ProbeFTP(host string, port int, timeout time.Duration) error {
	addr := fmt.Sprintf("%s:%d", host, port)
	conn, err := ftp.Dial(addr, ftp.DialWithTimeout(timeout))
	if err != nil {
		return fmt.Errorf("FTP probe failed: %w", err)
	}
	conn.Quit()
	return nil
}
