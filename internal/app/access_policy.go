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
	return a.accessPolicy(core, "")
}

func (a *App) RelayAccessPolicy(core, name string) (domain.AccessPolicy, error) {
	if name == "" {
		return domain.AccessPolicy{}, fmt.Errorf("请指定中转线路名称")
	}
	return a.accessPolicy(core, name)
}

func (a *App) accessPolicy(core, relay string) (domain.AccessPolicy, error) {
	if err := a.RootCheck(); err != nil {
		return domain.AccessPolicy{}, err
	}
	p, err := a.Registry.Get(core)
	if err != nil {
		return domain.AccessPolicy{}, err
	}
	unlock, err := a.lockAccessPolicy(context.Background(), core)
	if err != nil {
		return domain.AccessPolicy{}, err
	}
	defer unlock()
	if err := a.recoverAccessTransaction(context.Background(), p, core); err != nil {
		return domain.AccessPolicy{}, err
	}
	n, err := a.Store.Load(core)
	if err != nil {
		return domain.AccessPolicy{}, err
	}
	if relay == "" {
		return n.AccessPolicy.Normalize()
	}
	index, ok := findRelay(n, relay)
	if !ok {
		return domain.AccessPolicy{}, fmt.Errorf("找不到中转线路 %q", relay)
	}
	return n.RelayLinks[index].EffectiveAccessPolicy(n.AccessPolicy).Normalize()
}

func (a *App) SetAccessPolicy(ctx context.Context, core string, requested domain.AccessPolicy, expected ...domain.AccessPolicy) error {
	return a.setAccessPolicy(ctx, core, "", requested, expected...)
}

func (a *App) SetRelayAccessPolicy(ctx context.Context, core, name string, requested domain.AccessPolicy, expected ...domain.AccessPolicy) error {
	if name == "" {
		return fmt.Errorf("请指定中转线路名称")
	}
	return a.setAccessPolicy(ctx, core, name, requested, expected...)
}

func (a *App) setAccessPolicy(ctx context.Context, core, relay string, requested domain.AccessPolicy, expected ...domain.AccessPolicy) error {
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
	unlock, err := a.lockAccessPolicy(ctx, core)
	if err != nil {
		return err
	}
	defer unlock()
	if err := a.recoverAccessTransaction(ctx, p, core); err != nil {
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
	index := -1
	if relay != "" {
		var ok bool
		index, ok = findRelay(old, relay)
		if !ok {
			return fmt.Errorf("找不到中转线路 %q", relay)
		}
		current, err = old.RelayLinks[index].EffectiveAccessPolicy(old.AccessPolicy).Normalize()
	}
	if err != nil {
		return err
	}
	if len(expected) > 0 {
		base, err := expected[0].Normalize()
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current, base) {
			return fmt.Errorf("访问策略已被其他操作修改，请重新查看后重试")
		}
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
	if relay == "" {
		next.AccessPolicy = policy
	} else {
		next.RelayLinks = append([]domain.RelayLink(nil), old.RelayLinks...)
		next.RelayLinks[index].AccessPolicy = &policy
		next.RelayLinks[index].UpdatedAt = a.Now().UTC()
	}
	if !old.HasActiveAccessPolicy() && next.HasActiveAccessPolicy() {
		next.AccessPolicySniffing, err = accesspolicy.SnapshotSniffing(config, next)
		if err != nil {
			return err
		}
	}
	if !next.HasActiveAccessPolicy() {
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
	_, err = a.applyServerConfigTransaction(ctx, p, core, next, old, true, patched, managed, func() error {
		return a.beginAccessTransaction(ctx, p, core)
	})
	if err == nil {
		err = os.Remove(a.accessTransactionPath(core))
		if err == nil {
			return nil
		}
	}
	recoveryCtx, cancel := recoveryContext(ctx)
	defer cancel()
	if recoveryErr := a.recoverAccessTransaction(recoveryCtx, p, core); recoveryErr != nil {
		return recoveryResult(err, "", recoveryErr)
	}
	return err
}
