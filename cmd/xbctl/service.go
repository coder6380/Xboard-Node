package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const openRCName = "xboard-node"
const openRCPath = "/etc/init.d/xboard-node"
const openRCLog = "/var/log/xboard-node.log"

// serviceBackend 根据正在运行的初始化系统选择服务管理器。
func serviceBackend() (string, error) {
	if info, err := os.Stat("/run/systemd/system"); err == nil && info.IsDir() {
		if _, err := exec.LookPath("systemctl"); err == nil {
			return "systemd", nil
		}
	}
	if info, err := os.Stat("/run/openrc/softlevel"); err == nil && !info.IsDir() {
		if _, err := exec.LookPath("rc-service"); err == nil {
			if _, err := exec.LookPath("rc-update"); err == nil {
				return "openrc", nil
			}
		}
	}
	return "", fmt.Errorf("未检测到正在运行的 systemd 或 OpenRC")
}

// serviceCommand 根据服务管理器生成命令，不执行任何 shell 内容。
func serviceCommand(backend, action string, args []string) (string, []string, error) {
	switch backend {
	case "systemd":
		if action == "reload" {
			return "systemctl", []string{"daemon-reload"}, nil
		}
		if action == "logs" {
			if len(args) == 0 {
				args = []string{"-f"}
			}
			return "journalctl", append([]string{"-u", serviceName}, args...), nil
		}
		command := []string{action, serviceName}
		if action == "status" {
			command = append(command, "--no-pager")
		}
		return "systemctl", append(command, args...), nil
	case "openrc":
		if action == "reload" {
			return "", nil, nil
		}
		if action == "logs" {
			if len(args) == 0 {
				return "tail", []string{"-f", openRCLog}, nil
			}
			// 日志参数只接受两种后端共有的安全子集，避免误读其他文件。
			var tailArgs []string
			for i := 0; i < len(args); i++ {
				switch args[i] {
				case "-f", "--follow":
					tailArgs = append(tailArgs, "-f")
				case "-n", "--lines":
					i++
					if i >= len(args) || args[i] == "" || strings.Trim(args[i], "0123456789") != "" {
						return "", nil, fmt.Errorf("OpenRC 日志行数必须是非负整数")
					}
					tailArgs = append(tailArgs, "-n", args[i])
				default:
					return "", nil, fmt.Errorf("OpenRC 日志不支持参数 %q；支持 -f、--follow、-n 和 --lines", args[i])
				}
			}
			return "tail", append(tailArgs, openRCLog), nil
		}
		if action == "enable" || action == "disable" {
			verb := "add"
			if action == "disable" {
				verb = "del"
			}
			return "rc-update", append([]string{verb, openRCName, "default"}, args...), nil
		}
		return "rc-service", append([]string{openRCName, action}, args...), nil
	default:
		return "", nil, fmt.Errorf("未知服务管理器: %s", backend)
	}
}

// manageService 执行服务操作，非 root 用户使用 sudo。
func manageService(action string, args ...string) error {
	backend, err := serviceBackend()
	if err != nil {
		return err
	}
	name, command, err := serviceCommand(backend, action, args)
	if err != nil || name == "" {
		return err
	}
	if os.Geteuid() != 0 {
		command = append([]string{name}, command...)
		name = "sudo"
	}
	return runCommand(name, command...)
}

// credentialEnvironment 将凭据作为数据解析，拒绝无效键及多行注入。
func credentialEnvironment(data []byte) ([]string, error) {
	var result []string
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("凭据文件包含无效行")
		}
		for i, c := range key {
			if !(c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || i > 0 && c >= '0' && c <= '9') {
				return nil, fmt.Errorf("凭据文件包含无效变量名")
			}
		}
		if strings.ContainsAny(value, "\x00\r") {
			return nil, fmt.Errorf("凭据文件包含无效字符")
		}
		result = append(result, key+"="+value)
	}
	return result, nil
}

// startManagedProcess 读取凭据并以原样环境启动主程序。
func startManagedProcess() error {
	data, err := os.ReadFile(defaultCredentialsPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	credentials, err := credentialEnvironment(data)
	if err != nil {
		return err
	}
	return execManagedProcess(defaultBinaryPath, []string{defaultBinaryPath, "-c", defaultConfigPath}, append(os.Environ(), credentials...))
}
