package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"proxyforge/internal/app"
	"proxyforge/internal/provider"
	"proxyforge/internal/provider/singbox"
	"proxyforge/internal/provider/xray"
	"proxyforge/internal/selfupdate"
	"proxyforge/internal/system"
)

var errReturnToMenu = errors.New("返回主菜单")

type commandSet struct {
	app              *app.App
	in               io.Reader
	reader           *bufio.Reader
	out              io.Writer
	errOut           io.Writer
	yes              bool
	probeSNI         sniCandidateProbeFunc
	physicalIPs      func() ([]app.PublicInterfaceAddress, error)
	externalIP       func(context.Context) (string, error)
	interruptContext func(context.Context) (context.Context, context.CancelFunc)
	currentVersion   string
	selfUpdate       func(context.Context, selfupdate.Options) error
}

func New(version string) *cobra.Command {
	return newCommand(version, app.RequireRoot)
}

func newCommand(version string, rootCheck func() error) *cobra.Command {
	stdout := system.NewTerminalColorWriter(os.Stdout)
	stderr := system.NewTerminalColorWriter(os.Stderr)
	runner := &system.LoggingRunner{Runner: system.ExecRunner{Stdin: os.Stdin}, Out: stderr}
	layout := system.Layout{Root: os.Getenv("PROXYFORGE_ROOT")}
	reg := provider.NewRegistry(singbox.New(), xray.New())
	a := app.New(reg, runner, layout, stdout)
	a.RootCheck = rootCheck
	a.Progress = stderr
	a.Installer.Output = stderr
	selfInstaller := a.Installer
	selfInstaller.Runner = system.ExecRunner{Stdin: os.Stdin}
	updater := selfupdate.Updater{Installer: selfInstaller, Stdout: os.Stdout, Stderr: os.Stderr}
	c := &commandSet{
		app: a, in: os.Stdin, reader: bufio.NewReader(os.Stdin), out: stdout, errOut: stderr,
		probeSNI:    app.ProbeSNICandidates,
		physicalIPs: app.PhysicalInterfaceAddresses, externalIP: app.PublicAddress,
		currentVersion: strings.SplitN(version, "\n", 2)[0], selfUpdate: updater.Run,
	}
	root := &cobra.Command{
		Use: "proxyforge", Short: "Linux 双内核 VLESS + REALITY + Vision 管理器", Version: version,
		SilenceUsage: true, SilenceErrors: true,
		PersistentPreRunE: func(*cobra.Command, []string) error {
			fmt.Fprintln(c.errOut, "[步骤] 验证 root 运行权限")
			return rootCheck()
		},
		RunE: func(cmd *cobra.Command, args []string) error { return c.menu(cmd.Context()) },
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetIn(os.Stdin)
	root.SetVersionTemplate("{{.Name}} {{.Version}}\n")
	root.PersistentFlags().BoolVarP(&c.yes, "yes", "y", false, "非交互确认（内核管理脚本仍必须提供 SHA-256）")
	root.AddCommand(c.installCommand(), c.updateCommand(), c.uninstallCommand(), c.cleanupCommand(), c.configCommand(), c.serviceCommand())
	return root
}

func (c *commandSet) configCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "管理服务端、客户端和节点凭据"}
	cmd.AddCommand(c.generateCommand(), c.clientCommand(), c.resetCommand())
	return cmd
}
