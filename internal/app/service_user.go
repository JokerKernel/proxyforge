package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"proxyforge/internal/domain"
	"proxyforge/internal/system"
)

const XrayDedicatedServiceUser = "xray"

const xrayServiceUserDropIn = "/etc/systemd/system/xray.service.d/20-proxyforge-user.conf"
const xraySysusersConfig = "/usr/lib/sysusers.d/proxyforge-xray.conf"

var xraySysusersContent = []byte("# Managed by ProxyForge.\nu xray - \"Xray Service\" /nonexistent -\n")

type xrayServiceAccountOwnership struct {
	Version int    `json:"version"`
	UID     string `json:"uid"`
	GID     string `json:"gid"`
}

var xraySystemdUnits = []string{
	"/etc/systemd/system/xray.service",
	"/etc/systemd/system/xray@.service",
}

type ServiceUserChange struct {
	Previous    string
	Current     string
	Changed     bool
	Restarted   bool
	UserCreated bool
}

// XrayServiceUser returns the effective systemd user for the installed Xray
// service. An empty User= in systemd means root and is normalized by
// ServiceManager.UserState.
func (a *App) XrayServiceUser(ctx context.Context) (string, error) {
	if err := a.RootCheck(); err != nil {
		return "", err
	}
	p, err := a.Registry.Get(domain.CoreXray)
	if err != nil {
		return "", err
	}
	if err := a.checkCoreInstalled(ctx, p); err != nil {
		return "", err
	}
	return a.Services.UserState(ctx, p.ServiceName())
}

// UseDedicatedXrayServiceUser replaces the official installer's User=nobody
// assignment with a dedicated system account. The main unit is updated as well
// as a drop-in because merely overriding User= does not suppress systemd's
// warning while it parses the original User=nobody line.
func (a *App) UseDedicatedXrayServiceUser(ctx context.Context) (ServiceUserChange, error) {
	change := ServiceUserChange{}
	if err := a.RootCheck(); err != nil {
		return change, err
	}
	p, err := a.Registry.Get(domain.CoreXray)
	if err != nil {
		return change, err
	}
	if err := a.checkCoreInstalled(ctx, p); err != nil {
		return change, err
	}
	change.Previous, err = a.Services.UserState(ctx, p.ServiceName())
	if err != nil {
		return change, fmt.Errorf("读取 %s 运行用户: %w", p.ServiceName(), err)
	}

	status, statusErr := a.Services.IsActive(ctx, p.ServiceName())
	wasRunning, err := installedServiceRunning(status, statusErr)
	if err != nil {
		return change, fmt.Errorf("检查迁移前 %s 运行状态: %w", p.ServiceName(), err)
	}
	snapshots, changedUnits, err := a.prepareXrayServiceUserFiles()
	if err != nil {
		return change, err
	}
	configSnapshots, err := captureExistingMetadata(
		a.Layout.Resolve(filepath.Dir(p.ConfigPath())),
		a.Layout.Resolve(p.ConfigPath()),
		a.Layout.Resolve("/var/log/xray/access.log"),
		a.Layout.Resolve("/var/log/xray/error.log"),
	)
	if err != nil {
		return change, err
	}

	rollback := func(cause error) error {
		a.progressf("运行用户迁移失败，正在恢复 systemd unit 和文件权限")
		var rollbackErrors []error
		if err := restoreSnapshots(snapshots); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("恢复 systemd 配置文件: %w", err))
		}
		if err := restoreMetadataSnapshots(configSnapshots); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("恢复配置和日志权限: %w", err))
		}
		if err := a.Services.DaemonReload(ctx); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("恢复后刷新 systemd unit: %w", err))
		}
		if wasRunning {
			if err := a.Services.Restart(ctx, p.ServiceName()); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("恢复 %s 运行状态: %w", p.ServiceName(), err))
			}
		}
		if len(rollbackErrors) != 0 {
			return fmt.Errorf("%v；且回滚未完整完成: %w", cause, errors.Join(rollbackErrors...))
		}
		if change.UserCreated {
			return fmt.Errorf("%v；已恢复旧 systemd 设置；新创建的 xray 专用账号已安全保留", cause)
		}
		return fmt.Errorf("%v；已恢复旧 systemd 设置", cause)
	}

	a.progressf("将 Xray systemd unit 的运行用户设置为 %s", XrayDedicatedServiceUser)
	if err := writePreparedSnapshots(snapshots); err != nil {
		return change, rollback(err)
	}
	a.progressf("通过 systemd-sysusers 创建或检查 Xray 专用系统用户 %s", XrayDedicatedServiceUser)
	created, err := a.ensureXrayServiceAccount(ctx)
	change.UserCreated = created
	if err != nil {
		return change, rollback(err)
	}
	if created {
		if err := a.recordXrayServiceAccountOwnership(ctx); err != nil {
			return change, rollback(fmt.Errorf("记录新建的 xray 专用账号所有权: %w", err))
		}
	}
	if err := a.secureXrayFilesForDedicatedUser(ctx, p.ConfigPath()); err != nil {
		return change, rollback(err)
	}
	a.progressf("以 Xray 专用用户校验配置和引用文件权限")
	if err := a.validateXrayConfigAsDedicatedUser(ctx, p.ConfigPath()); err != nil {
		return change, rollback(err)
	}
	if changedUnits {
		a.progressf("刷新 systemd unit 缓存")
		if err := a.Services.DaemonReload(ctx); err != nil {
			return change, rollback(fmt.Errorf("刷新 systemd unit 失败: %w", err))
		}
	}
	change.Current, err = a.Services.UserState(ctx, p.ServiceName())
	if err != nil {
		return change, rollback(fmt.Errorf("核验 Xray 运行用户失败: %w", err))
	}
	if change.Current != XrayDedicatedServiceUser {
		return change, rollback(fmt.Errorf("Xray 有其他 systemd 配置覆盖了运行用户，当前仍为 %s", change.Current))
	}
	if wasRunning && (changedUnits || change.Previous != XrayDedicatedServiceUser) {
		a.progressf("重启 Xray 使专用运行用户立即生效")
		if err := a.Services.Restart(ctx, p.ServiceName()); err != nil {
			return change, rollback(fmt.Errorf("重启 %s 失败: %w", p.ServiceName(), err))
		}
		active, activeErr := a.Services.IsActive(ctx, p.ServiceName())
		if activeErr != nil || !active.Active {
			return change, rollback(fmt.Errorf("%s 未恢复 active 状态: %s: %w", p.ServiceName(), active.Detail, activeErr))
		}
		change.Restarted = true
	}
	change.Changed = changedUnits || change.UserCreated || change.Previous != XrayDedicatedServiceUser
	return change, nil
}

func (a *App) ensureXrayServiceAccount(ctx context.Context) (bool, error) {
	_, lookupErr := a.Runner.Run(ctx, "getent", "passwd", XrayDedicatedServiceUser)
	existed := lookupErr == nil
	if _, err := a.Runner.Run(ctx, "systemd-sysusers", a.Layout.Resolve(xraySysusersConfig)); err != nil {
		return !existed, fmt.Errorf("创建或检查系统用户 %s: %w", XrayDedicatedServiceUser, err)
	}
	if err := a.validateXrayServiceAccount(ctx); err != nil {
		return !existed, err
	}
	return !existed, nil
}

func (a *App) validateXrayServiceAccount(ctx context.Context) error {
	_, err := a.inspectXrayServiceAccount(ctx)
	return err
}

func (a *App) inspectXrayServiceAccount(ctx context.Context) (xrayServiceAccountOwnership, error) {
	var identity xrayServiceAccountOwnership
	passwdOutput, err := a.Runner.Run(ctx, "getent", "passwd", XrayDedicatedServiceUser)
	if err != nil {
		return identity, fmt.Errorf("未找到系统用户 %s: %w", XrayDedicatedServiceUser, err)
	}
	fields := strings.Split(strings.TrimSpace(string(passwdOutput)), ":")
	if len(fields) != 7 || fields[0] != XrayDedicatedServiceUser {
		return identity, fmt.Errorf("系统用户 %s 的 passwd 记录格式无效", XrayDedicatedServiceUser)
	}
	uid, uidErr := strconv.ParseUint(fields[2], 10, 32)
	_, gidErr := strconv.ParseUint(fields[3], 10, 32)
	if uidErr != nil || gidErr != nil {
		return identity, fmt.Errorf("系统用户 %s 的 UID/GID 无效", XrayDedicatedServiceUser)
	}
	if uid == 0 {
		return identity, fmt.Errorf("拒绝使用 UID 0 的 %s 用户运行 Xray", XrayDedicatedServiceUser)
	}
	home := filepath.Clean(fields[5])
	if home != "/nonexistent" {
		return identity, fmt.Errorf("已存在的 %s 用户不是 ProxyForge 专用服务账号：home=%s，要求 /nonexistent", XrayDedicatedServiceUser, fields[5])
	}
	shellBase := filepath.Base(strings.TrimSpace(fields[6]))
	if shellBase != "nologin" && shellBase != "false" {
		return identity, fmt.Errorf("已存在的 %s 用户允许登录：shell=%s", XrayDedicatedServiceUser, fields[6])
	}
	groupOutput, err := a.Runner.Run(ctx, "getent", "group", XrayDedicatedServiceUser)
	if err != nil {
		return identity, fmt.Errorf("未找到系统组 %s: %w", XrayDedicatedServiceUser, err)
	}
	groupFields := strings.Split(strings.TrimSpace(string(groupOutput)), ":")
	if len(groupFields) < 3 || groupFields[0] != XrayDedicatedServiceUser || groupFields[2] != fields[3] {
		return identity, fmt.Errorf("系统用户 %s 的主组不是同名专用组", XrayDedicatedServiceUser)
	}
	identity.Version = 1
	identity.UID = fields[2]
	identity.GID = fields[3]
	return identity, nil
}

func (a *App) recordXrayServiceAccountOwnership(ctx context.Context) error {
	identity, err := a.inspectXrayServiceAccount(ctx)
	if err != nil {
		return err
	}
	data, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	return system.AtomicWrite(a.Layout.XrayServiceAccountMarkerPath(), append(data, '\n'), 0600)
}

// cleanupOwnedXrayServiceAccount removes only an account whose creation was
// recorded by ProxyForge and whose numeric identity is unchanged. Older
// installations without the ownership marker are deliberately left alone.
func (a *App) cleanupOwnedXrayServiceAccount(ctx context.Context) error {
	markerPath := a.Layout.XrayServiceAccountMarkerPath()
	data, err := os.ReadFile(markerPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取 Xray 专用账号所有权标记: %w", err)
	}
	var recorded xrayServiceAccountOwnership
	if err := json.Unmarshal(data, &recorded); err != nil || recorded.Version != 1 || recorded.UID == "" || recorded.GID == "" {
		return fmt.Errorf("Xray 专用账号所有权标记无效，拒绝删除账号")
	}

	passwdOutput, passwdErr := a.Runner.Run(ctx, "getent", "passwd", XrayDedicatedServiceUser)
	groupOutput, groupErr := a.Runner.Run(ctx, "getent", "group", XrayDedicatedServiceUser)
	if passwdErr == nil {
		current, inspectErr := a.inspectXrayServiceAccount(ctx)
		if inspectErr != nil {
			return fmt.Errorf("核验待删除的 Xray 专用账号: %w", inspectErr)
		}
		if current.UID != recorded.UID || current.GID != recorded.GID {
			return fmt.Errorf("xray 账号身份已变化（当前 UID:GID=%s:%s，记录=%s:%s），拒绝删除",
				current.UID, current.GID, recorded.UID, recorded.GID)
		}
		if _, err := a.Runner.Run(ctx, "userdel", XrayDedicatedServiceUser); err != nil {
			return fmt.Errorf("删除 ProxyForge 创建的 xray 系统用户: %w", err)
		}
		if _, err := a.Runner.Run(ctx, "getent", "passwd", XrayDedicatedServiceUser); err == nil {
			return fmt.Errorf("删除 xray 系统用户后账号仍存在")
		}
		groupOutput, groupErr = a.Runner.Run(ctx, "getent", "group", XrayDedicatedServiceUser)
	} else if strings.TrimSpace(string(passwdOutput)) != "" {
		return fmt.Errorf("无法可靠确认 xray 系统用户是否存在，拒绝删除")
	}

	if groupErr == nil {
		fields := strings.Split(strings.TrimSpace(string(groupOutput)), ":")
		if len(fields) < 3 || fields[0] != XrayDedicatedServiceUser || fields[2] != recorded.GID {
			return fmt.Errorf("xray 系统组身份与所有权标记不符，拒绝删除")
		}
		if _, err := a.Runner.Run(ctx, "groupdel", XrayDedicatedServiceUser); err != nil {
			return fmt.Errorf("删除 ProxyForge 创建的 xray 系统组: %w", err)
		}
		if _, err := a.Runner.Run(ctx, "getent", "group", XrayDedicatedServiceUser); err == nil {
			return fmt.Errorf("删除 xray 系统组后组仍存在")
		}
	}
	if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除 Xray 专用账号所有权标记: %w", err)
	}
	return nil
}

type fileSnapshot struct {
	path     string
	old      []byte
	prepared []byte
	metadata fileMetadata
	existed  bool
}

func (a *App) prepareXrayServiceUserFiles() ([]fileSnapshot, bool, error) {
	var snapshots []fileSnapshot
	changed := false
	for index, unit := range xraySystemdUnits {
		path := a.Layout.Resolve(unit)
		old, err := os.ReadFile(path)
		if os.IsNotExist(err) && index > 0 {
			continue
		}
		if err != nil {
			return nil, false, fmt.Errorf("读取 Xray systemd unit %s: %w", path, err)
		}
		metadata, err := readMetadata(path, true)
		if err != nil {
			return nil, false, err
		}
		prepared, unitChanged, err := replaceSystemdServiceUser(old, XrayDedicatedServiceUser)
		if err != nil {
			return nil, false, fmt.Errorf("更新 %s: %w", path, err)
		}
		snapshots = append(snapshots, fileSnapshot{path: path, old: old, prepared: prepared, metadata: metadata, existed: true})
		changed = changed || unitChanged
	}

	dropInPath := a.Layout.Resolve(xrayServiceUserDropIn)
	dropInOld, readErr := os.ReadFile(dropInPath)
	dropInExisted := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return nil, false, readErr
	}
	dropInMetadata, err := readMetadata(dropInPath, dropInExisted)
	if err != nil {
		return nil, false, err
	}
	dropIn := []byte("# Managed by ProxyForge.\n[Service]\nUser=xray\nGroup=xray\n")
	snapshots = append(snapshots, fileSnapshot{
		path: dropInPath, old: dropInOld, prepared: dropIn, metadata: dropInMetadata, existed: dropInExisted,
	})
	changed = changed || !bytes.Equal(dropInOld, dropIn)

	sysusersPath := a.Layout.Resolve(xraySysusersConfig)
	sysusersOld, readErr := os.ReadFile(sysusersPath)
	sysusersExisted := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return nil, false, readErr
	}
	sysusersMetadata, err := readMetadata(sysusersPath, sysusersExisted)
	if err != nil {
		return nil, false, err
	}
	snapshots = append(snapshots, fileSnapshot{
		path: sysusersPath, old: sysusersOld, prepared: xraySysusersContent, metadata: sysusersMetadata, existed: sysusersExisted,
	})
	changed = changed || !bytes.Equal(sysusersOld, xraySysusersContent)
	return snapshots, changed, nil
}

func replaceSystemdServiceUser(data []byte, username string) ([]byte, bool, error) {
	lines := strings.SplitAfter(string(data), "\n")
	section := ""
	found := false
	changed := false
	for index, line := range lines {
		body := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		trimmed := strings.TrimSpace(body)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]"))
			continue
		}
		if section != "Service" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		key, _, hasValue := strings.Cut(trimmed, "=")
		if !hasValue || strings.TrimSpace(key) != "User" {
			continue
		}
		found = true
		ending := ""
		if strings.HasSuffix(line, "\r\n") {
			ending = "\r\n"
		} else if strings.HasSuffix(line, "\n") {
			ending = "\n"
		}
		indent := body[:len(body)-len(strings.TrimLeft(body, " \t"))]
		replacement := indent + "User=" + username + ending
		if replacement != line {
			lines[index] = replacement
			changed = true
		}
	}
	if !found {
		return nil, false, fmt.Errorf("[Service] 中未找到 User= 配置")
	}
	return []byte(strings.Join(lines, "")), changed, nil
}

func writePreparedSnapshots(snapshots []fileSnapshot) error {
	for _, snapshot := range snapshots {
		if bytes.Equal(snapshot.old, snapshot.prepared) {
			continue
		}
		mode := snapshot.metadata.mode
		if !snapshot.existed || mode == 0 {
			mode = 0644
		}
		if err := os.MkdirAll(filepath.Dir(snapshot.path), 0755); err != nil {
			return err
		}
		if err := system.AtomicWrite(snapshot.path, snapshot.prepared, mode); err != nil {
			return err
		}
		if snapshot.existed {
			if err := os.Chown(snapshot.path, snapshot.metadata.uid, snapshot.metadata.gid); err != nil {
				return err
			}
		}
	}
	return nil
}

func restoreSnapshots(snapshots []fileSnapshot) error {
	var firstErr error
	for index := len(snapshots) - 1; index >= 0; index-- {
		snapshot := snapshots[index]
		if err := restoreFile(snapshot.path, snapshot.old, snapshot.existed, snapshot.metadata); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

type metadataSnapshot struct {
	path     string
	metadata fileMetadata
}

func captureExistingMetadata(paths ...string) ([]metadataSnapshot, error) {
	var snapshots []metadataSnapshot
	for _, path := range paths {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		metadata, err := readMetadata(path, true)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, metadataSnapshot{path: path, metadata: metadata})
	}
	return snapshots, nil
}

func restoreMetadataSnapshots(snapshots []metadataSnapshot) error {
	var firstErr error
	for _, snapshot := range snapshots {
		if err := os.Chown(snapshot.path, snapshot.metadata.uid, snapshot.metadata.gid); err != nil && firstErr == nil {
			firstErr = err
		}
		if err := os.Chmod(snapshot.path, snapshot.metadata.mode); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (a *App) secureXrayFilesForDedicatedUser(ctx context.Context, configPath string) error {
	config := a.Layout.Resolve(configPath)
	if _, err := os.Stat(config); err == nil {
		configDir := filepath.Dir(config)
		for _, command := range [][]string{
			{"chown", "root:xray", configDir},
			{"chmod", "0750", configDir},
			{"chown", "root:xray", config},
			{"chmod", "0640", config},
		} {
			if _, runErr := a.Runner.Run(ctx, command[0], command[1:]...); runErr != nil {
				return fmt.Errorf("设置 Xray 配置权限: %w", runErr)
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	for _, path := range []string{"/var/log/xray/access.log", "/var/log/xray/error.log"} {
		resolved := a.Layout.Resolve(path)
		if _, err := os.Stat(resolved); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if _, err := a.Runner.Run(ctx, "chown", "xray:xray", resolved); err != nil {
			return fmt.Errorf("设置 Xray 日志权限: %w", err)
		}
		if _, err := a.Runner.Run(ctx, "chmod", "0600", resolved); err != nil {
			return fmt.Errorf("设置 Xray 日志权限: %w", err)
		}
	}
	return nil
}

func (a *App) validateXrayConfigAsDedicatedUser(ctx context.Context, configPath string) error {
	resolvedConfig := a.Layout.Resolve(configPath)
	if _, err := os.Stat(resolvedConfig); os.IsNotExist(err) {
		return fmt.Errorf("未找到 Xray 配置 %s，无法验证专用用户权限", resolvedConfig)
	} else if err != nil {
		return err
	}
	binary := a.Layout.Resolve("/usr/local/bin/xray")
	if a.Layout.Root == "" || a.Layout.Root == "/" {
		binary = "/usr/local/bin/xray"
	}
	if _, err := a.Runner.Run(ctx, "runuser", "-u", XrayDedicatedServiceUser, "--",
		binary, "run", "-test", "-config", resolvedConfig); err != nil {
		return fmt.Errorf("以 %s 用户校验 Xray 配置失败，请检查配置引用的证书、密钥和数据文件权限: %w",
			XrayDedicatedServiceUser, err)
	}
	return nil
}
