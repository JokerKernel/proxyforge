package xray

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

var supportedLogLevels = []string{"debug", "info", "warning", "error", "off"}

func (*Provider) LogLevels() []string {
	return append([]string(nil), supportedLogLevels...)
}

func (*Provider) CurrentLogLevel(config []byte) (string, error) {
	_, log, err := parseLogObject(config, "读取")
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
	root, log, err := parseLogObject(config, "修改")
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

func parseLogObject(config []byte, operation string) (map[string]any, map[string]any, error) {
	var root map[string]any
	if err := json.Unmarshal(config, &root); err != nil {
		return nil, nil, fmt.Errorf("%s现有 Xray 配置: %w", operation, err)
	}
	if root == nil {
		return nil, nil, fmt.Errorf("%s现有 Xray 配置: 顶层不是 JSON 对象", operation)
	}
	raw, exists := root["log"]
	if !exists || raw == nil {
		return root, nil, nil
	}
	log, ok := raw.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("现有 Xray log 不是对象")
	}
	return root, log, nil
}
