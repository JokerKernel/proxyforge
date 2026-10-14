package app

import (
	"context"
	"fmt"
	"os"
	"reflect"

	"proxyforge/internal/domain"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/accesspolicy"
	"proxyforge/internal/system"
)

func (a *App) AccessPolicy(core string) (domain.AccessPolicy, error) {
	if err := a.RootCheck(); err != nil {
		return domain.AccessPolicy{}, err
	}
	n, err := a.Store.Load(core)
	if err != nil {
		return domain.AccessPolicy{}, err
	}
	return n.AccessPolicy.Normalize()
}

func (a *App) SetAccessPolicy(ctx context.Context, core string, requested domain.AccessPolicy) error {
	if err := a.RootCheck(); err != nil {
		return err
	}
	policy, err := requested.Normalize()
	if err != nil {
		return err
	}
	p, err := a.Registry.Get(core)
	if err != nil {
		return err
	}
	if err := a.checkCoreInstalled(ctx, p); err != nil {
		return err
	}
	old, err := a.Store.Load(core)
	if err != nil {
		return err
	}
	current, err := old.AccessPolicy.Normalize()
	if err != nil {
		return err
	}
	if reflect.DeepEqual(current, policy) {
		return nil
	}
	handler, ok := p.(provider.AccessPolicyProvider)
	if !ok {
		return fmt.Errorf("%s 不支持访问策略", core)
	}
	config, err := os.ReadFile(a.Layout.Resolve(p.ConfigPath()))
	if err != nil {
		return err
	}
	next := old
	next.AccessPolicy = policy
	if !old.AccessPolicy.Active() && policy.Active() {
		next.AccessPolicySniffing, err = accesspolicy.SnapshotSniffing(config, next)
		if err != nil {
			return err
		}
	}
	if !policy.Active() {
		next.AccessPolicySniffing = nil
	}
	next.SchemaVersion = domain.StateSchemaVersion
	next.UpdatedAt = a.Now().UTC()
	patched, err := handler.PatchAccessPolicy(config, old, next)
	if err != nil {
		return err
	}
	managed := old.ConfigSHA256 != "" && old.ConfigSHA256 == system.SHA256(config)
	if policy.Disabled {
		a.progressf("关闭访问控制", "保留模式和名单，恢复原有拦截规则")
	} else {
		a.progressf("应用访问策略", "白名单优先，现有拦截规则兜底")
	}
	_, err = a.applyServerConfig(ctx, p, core, next, old, true, patched, managed)
	return err
}

func (a *App) SetAccessMode(ctx context.Context, core, mode string) error {
	policy, err := a.AccessPolicy(core)
	if err != nil {
		return err
	}
	policy.Mode = mode
	return a.SetAccessPolicy(ctx, core, policy)
}

func (a *App) SetAccessEnabled(ctx context.Context, core string, enabled bool) error {
	policy, err := a.AccessPolicy(core)
	if err != nil {
		return err
	}
	policy.Disabled = !enabled
	return a.SetAccessPolicy(ctx, core, policy)
}

func accessEntries(policy *domain.AccessPolicy, listName string) (*[]domain.AccessEntry, error) {
	switch listName {
	case "white":
		return &policy.Whitelist, nil
	case "black":
		return &policy.Blacklist, nil
	}
	return nil, fmt.Errorf("名单类型无效（可选 white 或 black）")
}

func (a *App) AddAccessEntry(ctx context.Context, core, listName, value string, exact bool) error {
	entry, err := domain.ParseAccessEntry(value, exact)
	if err != nil {
		return err
	}
	policy, err := a.AccessPolicy(core)
	if err != nil {
		return err
	}
	entries, err := accessEntries(&policy, listName)
	if err != nil {
		return err
	}
	for _, existing := range *entries {
		if existing == entry {
			return nil
		}
	}
	*entries = append(*entries, entry)
	return a.SetAccessPolicy(ctx, core, policy)
}

func (a *App) RemoveAccessEntry(ctx context.Context, core, listName, value string, exact bool) error {
	entry, err := domain.ParseAccessEntry(value, exact)
	if err != nil {
		return err
	}
	policy, err := a.AccessPolicy(core)
	if err != nil {
		return err
	}
	entries, err := accessEntries(&policy, listName)
	if err != nil {
		return err
	}
	for i, existing := range *entries {
		if existing == entry {
			*entries = append((*entries)[:i], (*entries)[i+1:]...)
			return a.SetAccessPolicy(ctx, core, policy)
		}
	}
	return fmt.Errorf("名单中不存在该规则: %s", value)
}
