package system

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strings"
)

func CheckPlatform(layout Layout) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("仅支持 Linux")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return fmt.Errorf("仅支持 amd64/arm64，当前为 %s", runtime.GOARCH)
	}
	b, err := os.ReadFile(layout.Resolve("/proc/1/comm"))
	if err != nil || strings.TrimSpace(string(b)) != "systemd" {
		return fmt.Errorf("PID 1 不是 systemd；不支持容器或其他 init 系统")
	}
	f, err := os.Open(layout.Resolve("/etc/os-release"))
	if err != nil {
		return fmt.Errorf("读取 os-release: %w", err)
	}
	defer f.Close()
	allowed := []string{"debian", "ubuntu", "rhel", "centos", "rocky", "almalinux", "fedora"}
	var id, like string
	s := bufio.NewScanner(f)
	for s.Scan() {
		parts := strings.SplitN(s.Text(), "=", 2)
		if len(parts) != 2 {
			continue
		}
		v := strings.Trim(parts[1], `"`)
		if parts[0] == "ID" {
			id = v
		}
		if parts[0] == "ID_LIKE" {
			like = v
		}
	}
	identifiers := append([]string{id}, strings.Fields(like)...)
	for _, v := range allowed {
		for _, candidate := range identifiers {
			if candidate == v {
				return nil
			}
		}
	}
	return fmt.Errorf("不支持的发行版 %q（ID_LIKE=%q）", id, like)
}
