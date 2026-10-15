package singbox

import (
	"fmt"
	"slices"
	"strings"

	"proxyforge/internal/provider/jsonutil"
)

var supportedLogLevels = []string{"trace", "debug", "info", "warn", "error", "fatal", "panic", "off"}

func (*Provider) LogLevels() []string {
	return append([]string(nil), supportedLogLevels...)
}

func (*Provider) CurrentLogLevel(config []byte) (string, error) {
	_, log, err := jsonutil.ParseOptionalObject(config, "sing-box", "读取", "log")
	if err != nil {
		return "", err
	}
	if log == nil {
		return "default", nil
	}
	if disabled, _ := log["disabled"].(bool); disabled {
		return "off", nil
	}
	level, _ := log["level"].(string)
	level = strings.ToLower(strings.TrimSpace(level))
	if level == "" {
		return "default", nil
	}
	if !slices.Contains(supportedLogLevels[:len(supportedLogLevels)-1], level) {
		return "", fmt.Errorf("现有 sing-box 日志级别 %q 不受支持", level)
	}
	return level, nil
}

func (*Provider) PatchLogLevel(config []byte, level string) ([]byte, error) {
	level = strings.ToLower(strings.TrimSpace(level))
	if !slices.Contains(supportedLogLevels, level) {
		return nil, fmt.Errorf("sing-box 日志级别 %q 无效", level)
	}
	root, log, err := jsonutil.ParseOptionalObject(config, "sing-box", "修改", "log")
	if err != nil {
		return nil, err
	}
	if log == nil {
		log = make(map[string]any)
		root["log"] = log
	}
	if level == "off" {
		log["disabled"] = true
	} else {
		log["disabled"] = false
		log["level"] = level
	}
	return marshalSingBox(root)
}
