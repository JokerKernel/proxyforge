package xray

import (
	"fmt"
	"slices"
	"strings"

	"proxyforge/internal/provider/jsonutil"
)

var supportedLogLevels = []string{"debug", "info", "warning", "error", "off"}

func (*Provider) LogLevels() []string {
	return append([]string(nil), supportedLogLevels...)
}

func (*Provider) CurrentLogLevel(config []byte) (string, error) {
	_, log, err := jsonutil.ParseOptionalObject(config, "Xray", "读取", "log")
	if err != nil {
		return "", err
	}
	if log == nil {
		return "warning", nil
	}
	level, _ := log["loglevel"].(string)
	level = strings.ToLower(strings.TrimSpace(level))
	if level == "none" {
		return "off", nil
	}
	if level == "" {
		return "warning", nil
	}
	if !slices.Contains(supportedLogLevels[:len(supportedLogLevels)-1], level) {
		return "", fmt.Errorf("现有 Xray 日志级别 %q 不受支持", level)
	}
	return level, nil
}

func (*Provider) PatchLogLevel(config []byte, level string) ([]byte, error) {
	level = strings.ToLower(strings.TrimSpace(level))
	if !slices.Contains(supportedLogLevels, level) {
		return nil, fmt.Errorf("Xray 日志级别 %q 无效", level)
	}
	root, log, err := jsonutil.ParseOptionalObject(config, "Xray", "修改", "log")
	if err != nil {
		return nil, err
	}
	if log == nil {
		log = make(map[string]any)
		root["log"] = log
	}
	if level == "off" {
		log["loglevel"] = "none"
	} else {
		log["loglevel"] = level
	}
	return marshalXray(root)
}
