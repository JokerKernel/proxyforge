package selfupdate

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"proxyforge/internal/system"
)

type downloadDisplay struct {
	out         io.Writer
	interactive bool
	color       bool
	width       int
	lastRefresh time.Time
	rendered    bool
}

func newDownloadDisplay(out io.Writer) *downloadDisplay {
	display := &downloadDisplay{
		out:         out,
		interactive: !strings.EqualFold(os.Getenv("TERM"), "dumb") && system.WriterInteractive(out),
		color:       system.TerminalColorEnabled(out),
		width:       50,
	}
	if terminal, ok := out.(*os.File); ok && display.interactive {
		command := exec.Command("stty", "size")
		command.Stdin = terminal
		if size, err := command.Output(); err == nil {
			fields := strings.Fields(string(size))
			if len(fields) == 2 {
				if columns, err := strconv.Atoi(fields[1]); err == nil && columns > 0 {
					display.width = min(50, max(0, columns-30))
				}
			}
		}
	}
	return display
}

func (d *downloadDisplay) paint(color, value string) string {
	if d.color {
		return color + value + "\x1b[0m"
	}
	return value
}

func (d *downloadDisplay) update(received, total int64) {
	if !d.interactive {
		return
	}
	now := time.Now()
	if now.Sub(d.lastRefresh) < 100*time.Millisecond {
		return
	}
	d.lastRefresh = now
	d.rendered = true
	if total <= 0 {
		fmt.Fprintf(d.out, "\r已下载 %s\x1b[K", downloadSize(received))
		return
	}
	percent := min(99, int(received*100/total))
	d.bar(percent, received, total)
}

func (d *downloadDisplay) finish(received int64) {
	if d.interactive {
		d.bar(100, received, received)
		fmt.Fprintln(d.out)
		return
	}
	fmt.Fprintf(d.out, "✓ 下载完成 · ProxyForge 更新脚本 · %d 字节\n", received)
}

func (d *downloadDisplay) bar(percent int, received, total int64) {
	filled := d.width * percent / 100
	bar := d.paint("\x1b[38;5;208m", strings.Repeat("■", filled)) +
		d.paint("\x1b[2m", strings.Repeat("･", d.width-filled))
	if d.width > 0 {
		bar += " "
	}
	fmt.Fprintf(d.out, "\r%s%3d%%  %s / %s\x1b[K", bar, percent, downloadSize(received), downloadSize(total))
}

func downloadSize(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	value := float64(size)
	for _, unit := range []string{"KiB", "MiB", "GiB"} {
		value /= 1024
		if value < 1024 {
			return fmt.Sprintf("%.1f %s", value, unit)
		}
	}
	return fmt.Sprintf("%.1f TiB", value/1024)
}
