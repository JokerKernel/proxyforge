package system

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

func UUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func ShortID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func ValidateServer(server string) error {
	server = strings.TrimSpace(server)
	if server == "" {
		return fmt.Errorf("server 不能为空")
	}
	if net.ParseIP(server) == nil {
		if strings.ContainsAny(server, "/:@") {
			return fmt.Errorf("server 必须是 IP 或主机名，不含协议和端口")
		}
		if !validHostname(server, false) {
			return fmt.Errorf("server 主机名无效")
		}
	}
	return nil
}

func ValidatePort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("端口必须在 1..65535")
	}
	return nil
}

func ValidateUserName(name string) error {
	if name == "" {
		return fmt.Errorf("用户名称不能为空")
	}
	if strings.TrimSpace(name) != name {
		return fmt.Errorf("用户名称首尾不能包含空白")
	}
	if utf8.RuneCountInString(name) > 128 {
		return fmt.Errorf("用户名称不能超过 128 个字符")
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return fmt.Errorf("用户名称不能包含控制字符")
	}
	return nil
}

func ValidateTag(tag string) error {
	if tag == "" {
		return fmt.Errorf("入站标签不能为空")
	}
	if strings.TrimSpace(tag) != tag {
		return fmt.Errorf("入站标签首尾不能包含空白")
	}
	if utf8.RuneCountInString(tag) > 128 {
		return fmt.Errorf("入站标签不能超过 128 个字符")
	}
	if strings.IndexFunc(tag, unicode.IsControl) >= 0 {
		return fmt.Errorf("入站标签不能包含控制字符")
	}
	return nil
}

func ValidateSNI(sni string) error {
	if strings.TrimSpace(sni) == "" || net.ParseIP(sni) != nil || !validHostname(sni, true) {
		return fmt.Errorf("SNI 必须是有效域名")
	}
	return nil
}

func validHostname(host string, requireDot bool) bool {
	if len(host) == 0 || len(host) > 253 || strings.TrimSpace(host) != host || strings.ContainsAny(host, "/:@_") {
		return false
	}
	if requireDot && !strings.Contains(host, ".") {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-') {
				return false
			}
		}
	}
	return true
}

func ValidateTarget(target string) error {
	host, p, err := net.SplitHostPort(target)
	if err != nil {
		return fmt.Errorf("target 必须为 host:port: %w", err)
	}
	if host == "" {
		return fmt.Errorf("target 主机不能为空")
	}
	port, err := strconv.Atoi(p)
	if err != nil {
		return fmt.Errorf("target 端口无效")
	}
	return ValidatePort(port)
}
