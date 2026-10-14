package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"runtime"
	"strings"

	"proxyforge/internal/docker/model"
)

var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-.][0-9A-Za-z]+)*$`)

func normalizeVersion(version string) (string, error) {
	v := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if !versionPattern.MatchString(v) {
		return "", fmt.Errorf("版本格式无效，请输入官方具体版本，如 26.3.27")
	}
	return v, nil
}

func normalizeImageSelection(version string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(version)) {
	case "", "stable", "latest":
		return "latest", nil
	case "pre-release", "prerelease":
		return "pre-release", nil
	default:
		return normalizeVersion(version)
	}
}

func checkPreparedVersion(md Metadata, version string) error {
	selection, err := normalizeImageSelection(version)
	if err != nil {
		return err
	}
	matched := false
	switch selection {
	case "latest":
		matched = md.Channel == "latest"
	case "pre-release":
		matched = md.Channel == "prerelease"
	default:
		matched = selection == md.RequestedVersion
	}
	if matched {
		return nil
	}
	return fmt.Errorf("镜像已准备为 %s（%s），请先通过升级入口切换版本", md.RequestedVersion, md.Channel)
}

func (m *Manager) prepareImage(ctx context.Context, version string) (Metadata, error) {
	v, err := normalizeImageSelection(version)
	if err != nil {
		return Metadata{}, err
	}
	channel := "pinned"
	switch v {
	case "latest":
		channel = "latest"
	case "pre-release":
		channel = "prerelease"
	}
	tag := Repository + ":" + v
	m.progress("拉取官方 Xray 镜像 %s", tag)
	if err = m.dockerStream(ctx, m.out, m.errOut, "image", "pull", tag); err != nil {
		return Metadata{}, fmt.Errorf("拉取官方版本镜像失败: %w", err)
	}
	if err = m.pauseAfterOutput(); err != nil {
		return Metadata{}, err
	}
	b, err := m.docker(ctx, "image", "inspect", tag)
	if err != nil {
		return Metadata{}, err
	}
	var images []struct {
		ID           string
		RepoDigests  []string
		Architecture string
		Os           string
	}
	if err = json.Unmarshal(b, &images); err != nil || len(images) != 1 {
		return Metadata{}, fmt.Errorf("镜像详情无效")
	}
	image := images[0]
	if !validImageID(image.ID) || image.Architecture != runtime.GOARCH || image.Os != "linux" {
		return Metadata{}, fmt.Errorf("镜像 ID 或平台不符合本机 Linux %s", runtime.GOARCH)
	}
	ref := ""
	for _, r := range image.RepoDigests {
		if validImageRef(r) {
			ref = r
			break
		}
	}
	if ref == "" {
		return Metadata{}, fmt.Errorf("镜像缺少官方仓库 digest")
	}
	output, err := m.coreRun(ctx, image.ID, nil, "version")
	if err != nil {
		return Metadata{}, fmt.Errorf("镜像版本检测失败: %w", err)
	}
	line := strings.Split(strings.TrimSpace(string(output)), "\n")[0]
	fields := strings.Fields(line)
	if len(fields) < 2 || fields[0] != "Xray" {
		return Metadata{}, fmt.Errorf("镜像实际版本输出无效")
	}
	actualVersion, err := normalizeVersion(fields[1])
	if err != nil {
		return Metadata{}, fmt.Errorf("镜像实际版本输出无效: %w", err)
	}
	if channel == "pinned" && actualVersion != v {
		return Metadata{}, fmt.Errorf("镜像实际版本与请求版本不一致")
	}
	if _, err = m.keyPair(ctx, image.ID); err != nil {
		return Metadata{}, err
	}
	md := Metadata{SchemaVersion: 1, ManagedBy: "proxyforge", InstanceID: Instance, ImageRef: ref, ImageID: image.ID, Version: line, RequestedVersion: actualVersion, Channel: channel, DesiredState: "stopped", UpdatedAt: m.now().UTC()}
	return md, nil
}
func parseKeyPair(b []byte) (model.KeyPair, error) {
	var pair model.KeyPair
	for _, line := range strings.Split(string(b), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		values := strings.Fields(value)
		if len(values) == 0 {
			continue
		}
		key = strings.ToLower(strings.NewReplacer(" ", "", "_", "", "-", "").Replace(key))
		switch key {
		case "private", "privatekey":
			pair.Private = values[0]
		case "public", "publickey", "password", "password(publickey)":
			pair.Public = values[0]
		}
	}
	if pair.Private == "" || pair.Public == "" {
		return pair, fmt.Errorf("镜像 REALITY 密钥输出格式不受支持")
	}
	return pair, nil
}
func (m *Manager) keyPair(ctx context.Context, imageID string) (model.KeyPair, error) {
	b, err := m.coreRun(ctx, imageID, nil, "x25519")
	if err != nil {
		return model.KeyPair{}, fmt.Errorf("生成 REALITY 密钥失败: %w", err)
	}
	return parseKeyPair(b)
}
