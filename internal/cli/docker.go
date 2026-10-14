package cli

import (
	"context"
	"time"

	dockerfeature "proxyforge/internal/docker"
)

// Only registration, display and cross-module cleanup coordination live here.
// All Docker business and infrastructure are owned by internal/docker.
func (c *commandSet) dockerManager() *dockerfeature.Manager {
	if c.docker == nil {
		root := ""
		if c.app != nil {
			root = c.app.Layout.Root
		}
		c.docker = dockerfeature.New(dockerfeature.Options{Root: root, Out: c.out, ErrOut: c.errOut})
	}
	return c.docker
}
func (c *commandSet) dockerLabel(ctx context.Context) string {
	if c.docker == nil {
		return "未部署"
	}
	statusCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	s, _ := c.docker.Status(statusCtx)
	return s.Label
}
func (c *commandSet) cleanupTarget(ctx context.Context, target string) error {
	if target != "all" {
		return c.app.Cleanup(ctx, target)
	}
	if err := c.app.CheckCleanup(ctx, "all"); err != nil {
		return err
	}
	docker := c.dockerManager()
	lease, err := docker.BeginCleanup(ctx)
	if err != nil {
		return err
	}
	defer lease.Close()
	if err := c.app.Cleanup(ctx, "all"); err != nil {
		return err
	}
	return lease.Cleanup()
}
