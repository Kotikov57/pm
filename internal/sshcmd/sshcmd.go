package sshcmd

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

type Config struct {
	Host     string
	Port     int
	User     string
	Identity string
}

func (c Config) target() string {
	if c.User != "" {
		return fmt.Sprintf("%s@%s", c.User, c.Host)
	}
	return c.Host
}

func (c Config) sshArgs() []string {
	args := []string{}
	if c.Port != 0 {
		args = append(args, "-p", fmt.Sprintf("%d", c.Port))
	}
	if c.Identity != "" {
		args = append(args, "-i", c.Identity)
	}
	return args
}

func (c Config) scpArgs() []string {
	args := []string{}
	if c.Port != 0 {
		args = append(args, "-P", fmt.Sprintf("%d", c.Port))
	}
	if c.Identity != "" {
		args = append(args, "-i", c.Identity)
	}
	return args
}

func RunSSH(c Config, command string) (string, error) {
	if c.Host == "" {
		return "", fmt.Errorf("ssh host is required")
	}
	args := append(c.sshArgs(), c.target(), command)
	cmd := exec.Command("ssh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ssh command failed: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func UploadFile(c Config, localPath, remoteDir string) (string, error) {
	if c.Host == "" {
		return "", fmt.Errorf("ssh host is required")
	}
	if remoteDir != "" {
		if _, err := RunSSH(c, fmt.Sprintf("mkdir -p %s", ShellEscape(remoteDir))); err != nil {
			return "", err
		}
	}

	remotePath := filepath.Base(localPath)
	if remoteDir != "" {
		remotePath = path.Join(remoteDir, remotePath)
	}

	args := append(c.scpArgs(), localPath, fmt.Sprintf("%s:%s", c.target(), remotePath))
	cmd := exec.Command("scp", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("scp upload failed: %w", err)
	}
	return remotePath, nil
}

func DownloadFile(c Config, remotePath, localDir string) (string, error) {
	if c.Host == "" {
		return "", fmt.Errorf("ssh host is required")
	}
	if localDir == "" {
		localDir = "."
	}
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		return "", err
	}
	localPath := filepath.Join(localDir, filepath.Base(remotePath))
	args := append(c.scpArgs(), fmt.Sprintf("%s:%s", c.target(), remotePath), localPath)
	cmd := exec.Command("scp", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("scp download failed: %w", err)
	}
	return localPath, nil
}

func ShellEscape(p string) string {
	if p == "" {
		return ""
	}
	if !strings.ContainsAny(p, " '") {
		return p
	}
	return "'" + strings.ReplaceAll(p, "'", "'\\''") + "'"
}
