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

	"github.com/mogumc/oshind/types"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type SFTPDownloader struct {
}

// NewSFTPDownloader 创建新的 SFTP 下载器
func NewSFTPDownloader() *SFTPDownloader {
	return &SFTPDownloader{}
}

// Download 建立 SFTP 连接并下载文件（支持断点续传）
func (d *SFTPDownloader) Download(ctx context.Context, task *types.DownloadTask) error {
	host, port, path, err := d.parseSFTPAddress(task.URL)
	if err != nil {
		return fmt.Errorf("invalid SFTP address: %w", err)
	}

	ftpConfig := task.Config.FTPConfig
	if ftpConfig == nil {
		ftpConfig = &types.FTPConfig{Port: port}
	} else if ftpConfig.Port == 0 {
		ftpConfig.Port = port
	}

	sshConfig := &ssh.ClientConfig{
		User:            ftpConfig.Username,
		Auth:            []ssh.AuthMethod{},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // 注意：生产环境应该验证主机密钥
	}

	// 无凭据时使用匿名用户
	if sshConfig.User == "" {
		sshConfig.User = "anonymous"
	}

	if ftpConfig.Password != "" {
		sshConfig.Auth = append(sshConfig.Auth, ssh.Password(ftpConfig.Password))
	}

	addr := fmt.Sprintf("%s:%d", host, ftpConfig.Port)
	sshClient, err := ssh.Dial("tcp", addr, sshConfig)
	if err != nil {
		return fmt.Errorf("SSH connection failed: %w", err)
	}
	defer sshClient.Close()

	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		return fmt.Errorf("SFTP client creation failed: %w", err)
	}
	defer sftpClient.Close()

	remoteFile, err := sftpClient.Open(path)
	if err != nil {
		return fmt.Errorf("SFTP open failed: %w", err)
	}
	defer remoteFile.Close()

	fileInfo, err := remoteFile.Stat()
	if err != nil {
		return fmt.Errorf("SFTP stat failed: %w", err)
	}

	task.Metadata.Size = fileInfo.Size()
	task.Metadata.SupportResume = true // SFTP 支持断点续传

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
		// SFTP 单流下载，只比较文件大小
		if !d.validateSFTPResumeFile(existingState, tempPath) {
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

	// 打开或创建临时文件（支持续传）
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

	// SFTP 使用单流下载，通过 Seek 支持断点续传
	if resumeOffset > 0 {
		// Seek 到续传位置
		if _, err := remoteFile.Seek(resumeOffset, io.SeekStart); err != nil {
			return fmt.Errorf("SFTP seek failed: %w", err)
		}
		// 设置已下载进度
		task.Progress.AddDownloaded(resumeOffset)
	}

	buffer := make([]byte, 32*1024) // 32KB 缓冲区

	for {
		select {
		case <-ctx.Done():
			// 保存当前状态以便续传
			stateSaver.ForceSave()
			return ctx.Err()
		default:
		}

		n, readErr := remoteFile.Read(buffer)
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
			return fmt.Errorf("SFTP read failed: %w", readErr)
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

// validateSFTPResumeFile 校验 SFTP 续传文件的有效性
func (d *SFTPDownloader) validateSFTPResumeFile(state *OShinState, tempPath string) bool {
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

// parseSFTPAddress 从 URL 中提取主机、端口和路径
func (d *SFTPDownloader) parseSFTPAddress(rawURL string) (host string, port int, path string, err error) {
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
		port = 22 // SFTP 默认端口
	}

	path = u.Path
	if path == "" || path == "/" {
		path = "/"
	}

	return host, port, path, nil
}

// getOutputPath 获取输出文件路径
func (d *SFTPDownloader) getOutputPath(task *types.DownloadTask) string {
	if task.FileName != "" {
		return filepath.Join(task.Config.OutputDir, task.FileName)
	}

	fileName := ExtractFileInfo(task.URL)
	return filepath.Join(task.Config.OutputDir, fileName)
}

// ProbeSFTP 测试 SFTP 服务器的可达性
func ProbeSFTP(host string, port int, username, password string, timeout time.Duration) error {
	addr := fmt.Sprintf("%s:%d", host, port)

	sshConfig := &ssh.ClientConfig{
		User: username,
		Auth: []ssh.AuthMethod{
			ssh.Password(password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         timeout,
	}

	sshClient, err := ssh.Dial("tcp", addr, sshConfig)
	if err != nil {
		return fmt.Errorf("SFTP probe failed: %w", err)
	}
	defer sshClient.Close()

	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		return fmt.Errorf("SFTP client creation failed: %w", err)
	}
	defer sftpClient.Close()

	return nil
}
