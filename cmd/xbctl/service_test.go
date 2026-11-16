package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestConfigHealthPortMatchesFirstInstance 验证多实例及旧版配置的端口选择。
func TestConfigHealthPortMatchesFirstInstance(t *testing.T) {
	for _, tc := range []struct{ name, config, want string }{
		{"first-disabled", "health_port: 4444\ninstances:\n  - health_port: 0\n  - health_port: 1234\n", "0\n"},
		{"first-missing", "health_port: 4444\ninstances:\n  - {}\n  - health_port: 1234\n", "0\n"},
		{"first-enabled", "instances:\n  - health_port: 4321\n  - health_port: 1234\n", "4321\n"},
		{"legacy-enabled", "health_port: 5678\n", "5678\n"},
		{"legacy-disabled", "{}\n", "0\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yml")
			if err := os.WriteFile(path, []byte(tc.config), 0600); err != nil {
				t.Fatal(err)
			}
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			original := os.Stdout
			os.Stdout = writer
			err = runConfigHealthPort([]string{"--config", path})
			os.Stdout = original
			writer.Close()
			output, readErr := io.ReadAll(reader)
			reader.Close()
			if err != nil || readErr != nil || string(output) != tc.want {
				t.Fatalf("got %q, want %q: %v %v", output, tc.want, err, readErr)
			}
		})
	}
}

// TestCredentialEnvironment 验证特殊字符只作为原始数据读取。
func TestCredentialEnvironment(t *testing.T) {
	value := " token 'quoted' $(touch /tmp/never) `id` \\ end "
	got, err := credentialEnvironment([]byte("TOKEN=" + value + "\n"))
	if err != nil || !reflect.DeepEqual(got, []string{"TOKEN=" + value}) {
		t.Fatalf("got %q, err %v", got, err)
	}
	for _, input := range []string{"BAD-NAME=value\n", "1BAD=value\n", "TOKEN=x\ninjected\n", "TOKEN=x\x00y\n"} {
		if _, err := credentialEnvironment([]byte(input)); err == nil {
			t.Errorf("accepted invalid credentials %q", input)
		}
	}
}

// TestServiceCommand 验证两种服务管理器的命令映射。
func TestServiceCommand(t *testing.T) {
	for _, tc := range []struct {
		backend, action, name string
		args                  []string
	}{
		{"systemd", "restart", "systemctl", []string{"restart", serviceName}},
		{"openrc", "restart", "rc-service", []string{openRCName, "restart"}},
		{"openrc", "enable", "rc-update", []string{"add", openRCName, "default"}},
		{"openrc", "disable", "rc-update", []string{"del", openRCName, "default"}},
		{"openrc", "logs", "tail", []string{"-f", openRCLog}},
		{"systemd", "logs", "journalctl", []string{"-u", serviceName, "-f"}},
	} {
		name, args, err := serviceCommand(tc.backend, tc.action, nil)
		if err != nil || name != tc.name || !reflect.DeepEqual(args, tc.args) {
			t.Errorf("%s %s: got %s %q: %v", tc.backend, tc.action, name, args, err)
		}
	}
}

// TestOpenRCLogArguments 验证日志参数仅允许指定的安全子集。
func TestOpenRCLogArguments(t *testing.T) {
	for _, args := range [][]string{{"--since", "today"}, {"/etc/passwd"}, {"-n", "-1"}, {"-n"}, {"--lines=2"}} {
		if _, _, err := serviceCommand("openrc", "logs", args); err == nil {
			t.Errorf("accepted %q", args)
		}
	}
	name, args, err := serviceCommand("openrc", "logs", []string{"-n", "20", "-f"})
	if err != nil || name != "tail" || !reflect.DeepEqual(args, []string{"-n", "20", "-f", openRCLog}) {
		t.Fatalf("unexpected command %s %q: %v", name, args, err)
	}
	_, args, err = serviceCommand("systemd", "logs", []string{"--since", "today"})
	if err != nil || !reflect.DeepEqual(args, []string{"-u", serviceName, "--since", "today"}) {
		t.Fatalf("systemd arguments changed: %q %v", args, err)
	}
}

// TestConfigInitRejectsInvalidTokenBeforeWrites 验证拒绝非法凭据时不修改已有文件。
func TestConfigInitRejectsInvalidTokenBeforeWrites(t *testing.T) {
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "config.yml"), filepath.Join(dir, "credentials.env"), filepath.Join(dir, "meta.json")}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	err := runConfigInit([]string{"--mode", "node", "--panel-url", "https://example.com", "--node-id", "1", "--token", "bad\ntoken", "--config", paths[0], "--output", paths[0], "--credentials-in", paths[1], "--credentials-out", paths[1], "--meta", paths[2]})
	if err == nil {
		t.Fatal("accepted multiline token")
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "original" {
			t.Fatalf("modified %s: %q %v", path, data, err)
		}
	}
}

// TestInstallerHealthPortUsesStagedCLI 验证升级读取新解析器的零端口。
func TestInstallerHealthPortUsesStagedCLI(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	script, err := os.ReadFile(filepath.Join("..", "..", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(script), "\r\n", "\n")
	start := strings.Index(text, "load_health_port_from_config() {")
	if start < 0 {
		t.Fatal("health port function missing")
	}
	end := strings.Index(text[start:], "\n}\n")
	if end < 0 {
		t.Fatal("health port function incomplete")
	}
	function := text[start : start+end+2]
	testScript := function + `
log_error() { printf '%s\n' "$1" >&2; }
TMP_DIR="$1/staged"
CLI_PATH="$1/old"
HEALTH_PORT=65530
HEALTH_ENABLED=1
load_health_port_from_config "$1/config"
[ "$HEALTH_PORT" = 0 ] && [ "$HEALTH_ENABLED" = 0 ]
`
	// 使用临时桩模拟旧版本无输出及新版本输出零。
	command := exec.Command(bash, "-c", `d=$(mktemp -d); trap 'rm -rf "$d"' EXIT; mkdir "$d/staged"; touch "$d/config"; printf '#!/bin/sh\nexit 0\n' > "$d/old"; printf '#!/bin/sh\nprintf "0\\n"\n' > "$d/staged/xbctl"; chmod +x "$d/old" "$d/staged/xbctl"; bash -c "$1" _ "$d"`, "_", testScript)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, output)
	}
}

// TestMergeCredentialsRejectsNewline 防止凭据被拆成额外环境变量。
func TestMergeCredentialsRejectsNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.env")
	if err := mergeCredentials("", path, "TOKEN", "x\nOTHER=y"); err == nil {
		t.Fatal("accepted multiline token")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unexpected output file: %v", err)
	}
}
