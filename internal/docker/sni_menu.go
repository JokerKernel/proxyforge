package docker

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var defaultSNICandidates = []string{
	"www.bing.com", "th.bing.com", "ts1.tc.mm.bing.net", "ts3.tc.mm.bing.net", "ts4.tc.mm.bing.net", "r.bing.com",
	"www.xbox.com", "assets-www.xbox.com", "assets-xbxweb.xbox.com", "sisu.xboxlive.com",
	"azure.microsoft.com", "cdn-dynmedia-1.microsoft.com", "cdn77.api.userway.org", "c.s-microsoft.com",
	"acctcdn.msftauth.net", "aadcdn.msftauth.net", "configuration.ls.apple.com", "devblogs.microsoft.com",
	"img-prod-cms-rt-microsoft-com.akamaized.net", "aws.com", "aws.amazon.com", "www.aws.com",
	"s0.awsstatic.com", "d1.awsstatic.com", "t0.m.awsstatic.com", "a0.awsstatic.com",
	"prod.pa.cdn.uis.awsstatic.com", "a.b.cdn.console.awsstatic.com", "vs.aws.amazon.com",
	"prod.us-east-1.ui.gcr-chat.marketing.aws.dev", "amd.com", "www.amd.com", "download.amd.com", "drivers.amd.com",
	"intel.com", "downloadmirror.intel.com", "images.nvidia.com", "www.nvidia.com",
	"iosapps.itunes.apple.com", "se-edge.itunes.apple.com", "is1-ssl.mzstatic.com", "apps.mzstatic.com",
	"gsp-ssl.ls.apple.com", "www.sony.com", "electronics.sony.com", "digitalassets.tesla.com",
	"cua-chat-ui.tesla.com", "www.tesla.com", "catalog.gamepass.com", "assets.adobedtm.com",
	"cdn.bizibly.com", "tag-logger.demandbase.com", "tag.demandbase.com", "mscom.demdex.net",
	"logx.optimizely.com", "cdnssl.clicktale.net", "s.go-mpulse.net", "s.company-target.com",
	"beacon.gtv-pub.com", "snap.licdn.com", "j.6sc.co", "c.6sc.co", "b.6sc.co", "static.cloud.coveo.com",
	"ce.mf.marsflag.com", "s.mp.marsflag.com", "gray-config-prod.api.arc-cdn.net", "gray-wowt-prod.gtv-cdn.com",
	"www.wowt.com", "cdn.userway.org", "github.gallerycdn.vsassets.io", "ms-vscode.gallerycdn.vsassets.io",
	"ms-python.gallerycdn.vsassets.io", "academy.nvidia.com", "addons.mozilla.org", "aod.itunes.apple.com",
	"gateway.icloud.com", "download-installer.cdn.mozilla.net", "images-na.ssl-images-amazon.com", "itunes.apple.com",
	"lol.secure.dyn.riotcdn.net", "m.media-amazon.com", "mensura.cdn-apple.com", "osxapps.itunes.apple.com",
	"player.live-video.net", "software.download.prss.microsoft.com", "swcdn.apple.com", "swdist.apple.com",
	"updates.cdn-apple.com",
}

const sniResultPageSize = 20

const (
	sniPageActionSelect     = "select"
	sniPageActionNext       = "next"
	sniPageActionPrev       = "prev"
	sniPageActionGoto       = "goto"
	sniPageActionCancel     = "cancel"
	sniPageActionManual     = "manual"
	sniPageActionRetest     = "retest"
	sniPageActionBack       = "back"
	sniPageActionSortToggle = "sort-toggle"
	sniPageActionSortIPv4   = "sort-ipv4"
	sniPageActionSortIPv6   = "sort-ipv6"
)

type sniPageOptions struct {
	AllowManual bool
	AllowRetest bool
	AllowBack   bool
}

func (m *Manager) selectSNICandidate(ctx context.Context, u *ui, server string) (string, error) {
	u.clearScreen()
	u.pageHeader("REALITY SNI 候选检测")
	fmt.Fprintf(u.out, "正在并发测试 %d 个 SNI 候选的 DNS、TCP/TLS、ALPN、证书 SAN 和 CDN 特征，请稍候……\n", len(defaultSNICandidates))
	candidates, err := m.probeSNICandidates(ctx, defaultSNICandidates, server, len(defaultSNICandidates))
	if err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("SNI 测速没有返回可用候选")
	}
	u.drainBufferedNewlines()
	page := 0
	sortIPv4 := true
	for {
		sortSNICandidates(candidates, sortIPv4)
		u.clearScreen()
		u.pageHeader("REALITY SNI 候选检测")
		fmt.Fprintf(u.out, "全部有效候选（%s）：\n", sniSortLabel(sortIPv4))
		_, _, pages := u.printSNIResultPage(candidates, page, "")
		u.printSNIPageControls(page, pages, sortIPv4, sniPageOptions{AllowManual: true})
		fmt.Fprintln(u.out)
		fmt.Fprintln(u.out, "提示：以上候选均已通过 DNS、TLS 和证书名称校验；IPv4/IPv6 延迟为各自 TCP+TLS 探测耗时。")
		fmt.Fprintln(u.out, "      只能选择本页显示的编号。")
		action, index, chooseErr := u.readSNIPageChoice(len(candidates), page, pages, sniPageOptions{AllowManual: true})
		if chooseErr != nil {
			return "", chooseErr
		}
		if action == sniPageActionCancel {
			return "", errCancelled
		}
		if applySNISortAction(&sortIPv4, &page, action) {
			continue
		}
		switch action {
		case sniPageActionNext:
			page++
		case sniPageActionPrev:
			page--
		case sniPageActionGoto:
			page = index
		case sniPageActionSelect:
			return candidates[index-1].Domain, nil
		case sniPageActionManual:
			return u.askManualSNI(ctx, m, server)
		}
	}
}

func (u *ui) askManualSNI(ctx context.Context, m *Manager, server string) (string, error) {
	emptyShown := false
	for {
		domain, err := u.ask("请输入 REALITY SNI", "")
		if err != nil {
			return "", err
		}
		domain = strings.TrimSpace(domain)
		if domain != "" {
			if err = m.confirmManualSNI(ctx, u, domain, server); err != nil {
				return "", err
			}
			return domain, nil
		}
		if u.interactive {
			eraseChoiceRetry(u.out, false)
		}
		if emptyShown {
			continue
		}
		fmt.Fprintln(u.out, "SNI 不能为空。")
		emptyShown = true
	}
}

func (m *Manager) confirmManualSNI(ctx context.Context, u *ui, domain, server string) error {
	fmt.Fprintf(u.out, "正在检测手动 SNI %s 的 DNS、TCP/TLS、ALPN、证书 SAN 和 CDN 特征，请稍候……\n", domain)
	candidates, err := m.probeSNICandidates(ctx, []string{domain}, server, 1)
	if err != nil {
		return fmt.Errorf("手动 SNI %s 未通过检测: %w", domain, err)
	}
	if len(candidates) != 1 {
		return fmt.Errorf("手动 SNI %s 未返回有效检测结果", domain)
	}
	fmt.Fprintln(u.out, "手动 SNI 检测结果：")
	u.printSNICandidateDetails("域名：", candidates[0])
	fmt.Fprintln(u.out, "提示：检测结果只反映当前网络状态，不代表目标归属、长期可用性或使用授权。")
	return u.confirm("确认采用这个手动 SNI？")
}

func (m *Manager) retestSNICandidates(ctx context.Context, u *ui) error {
	state, err := m.LoadState()
	if err != nil {
		return err
	}
	currentSNI := strings.TrimSpace(state.Node.SNI)
	server := strings.TrimSpace(state.Node.Server)
	if currentSNI == "" || server == "" || state.Node.IsTLS() {
		return fmt.Errorf("当前受管节点缺少 Server 或 REALITY SNI，无法重新检测")
	}
	domains := make([]string, 0, len(defaultSNICandidates)+1)
	domains = append(domains, currentSNI)
	domains = append(domains, defaultSNICandidates...)
	for {
		u.clearScreen()
		u.pageHeader("Xray（Docker）", "REALITY SNI 候选重新检测")
		fmt.Fprintf(u.out, "节点地址：%s\n", server)
		fmt.Fprintf(u.out, "当前 SNI：%s\n\n", currentSNI)
		fmt.Fprintf(u.out, "正在并发重新测试当前 SNI 和 %d 个内置候选，请稍候……\n", len(defaultSNICandidates))
		results, probeErr := m.probeSNICandidates(ctx, domains, server, len(domains))
		if probeErr != nil {
			return fmt.Errorf("重新检测 REALITY SNI 候选: %w", probeErr)
		}
		sortIPv4 := true
		page := 0
		for {
			sortSNICandidates(results, sortIPv4)
			rank, current := sniCurrentRank(results, currentSNI)
			u.clearScreen()
			u.pageHeader("Xray（Docker）", "REALITY SNI 候选重新检测")
			fmt.Fprintf(u.out, "节点地址：%s\n", server)
			fmt.Fprintf(u.out, "当前 SNI：%s\n", currentSNI)
			if rank == 0 {
				fmt.Fprintln(u.out, "\n[警告] 当前 SNI 未通过本次 DNS、TLS 或证书名称校验。")
				fmt.Fprintln(u.out, "检测结果可能受临时网络波动影响，可立即重新测试；本次不会修改配置。")
			} else {
				fmt.Fprintf(u.out, "\n[结果] 当前 SNI 通过检测，在全部有效候选中排名第 %d。\n", rank)
				u.printSNICandidateDetails("当前 SNI：", current)
			}
			fmt.Fprintf(u.out, "\n全部有效候选（%s）：\n", sniSortLabel(sortIPv4))
			_, _, pages := u.printSNIResultPage(results, page, currentSNI)
			u.printSNIPageControls(page, pages, sortIPv4, sniPageOptions{AllowRetest: true, AllowBack: true})
			fmt.Fprintln(u.out, "\n提示：本操作只重新检测和展示结果，不会修改 SNI、target 或重启服务。")
			fmt.Fprintln(u.out, "      如需采用新候选，请返回后选择“重置 SNI/target”。")
			action, index, chooseErr := u.readSNIPageChoice(0, page, pages, sniPageOptions{AllowRetest: true, AllowBack: true})
			if chooseErr != nil {
				return chooseErr
			}
			if applySNISortAction(&sortIPv4, &page, action) {
				continue
			}
			switch action {
			case sniPageActionNext:
				page++
			case sniPageActionPrev:
				page--
			case sniPageActionGoto:
				page = index
			case sniPageActionRetest:
			case sniPageActionBack:
				return errCancelled
			}
			if action == sniPageActionRetest {
				break
			}
		}
	}
}

func sniCurrentRank(candidates []sniCandidate, currentSNI string) (int, sniCandidate) {
	for index, candidate := range candidates {
		if strings.EqualFold(candidate.Domain, currentSNI) {
			return index + 1, candidate
		}
	}
	return 0, sniCandidate{}
}

func (u *ui) printSNIResultPage(candidates []sniCandidate, page int, currentSNI string) (start, end, pages int) {
	start, end, pages = sniPageBounds(len(candidates), page, sniResultPageSize)
	fmt.Fprintf(u.out, "第 %d/%d 页，共 %d 个\n\n", page+1, pages, len(candidates))
	domainWidth := sniDomainColumnWidth(candidates[start:end])
	for index, candidate := range candidates[start:end] {
		marker := ""
		if currentSNI != "" && strings.EqualFold(candidate.Domain, currentSNI) {
			marker = "[当前 SNI]"
		}
		u.printSNICandidateSummary(start+index+1, candidate, marker, domainWidth)
	}
	return start, end, pages
}

func (u *ui) printSNIPageControls(page, pages int, sortIPv4 bool, opts sniPageOptions) {
	fmt.Fprintln(u.out)
	nav := make([]string, 0, 6)
	if opts.AllowRetest {
		nav = append(nav, "1 重新测试")
	}
	if sortIPv4 {
		nav = append(nav, "v IPv6排序")
	} else {
		nav = append(nav, "v IPv4排序")
	}
	if pages > 1 {
		if page+1 < pages {
			nav = append(nav, "n 下一页")
		}
		if page > 0 {
			nav = append(nav, "p 上一页")
		}
		nav = append(nav, "g/G 首/末页", "pN 跳页")
	}
	fmt.Fprintln(u.out, "  "+strings.Join(nav, "  ·  "))
	actions := make([]string, 0, 2)
	if opts.AllowManual {
		actions = append(actions, "0 手动输入", "q 返回")
	} else if opts.AllowBack {
		actions = append(actions, "0/q 返回")
	}
	if len(actions) > 0 {
		fmt.Fprintln(u.out, "  "+strings.Join(actions, "  ·  "))
	}
}

func (u *ui) readSNIPageChoice(total, page, pages int, opts sniPageOptions) (string, int, error) {
	prompt := "❯ 请选择"
	switch {
	case opts.AllowManual:
		prompt += "（本页编号或下方按键）"
	case opts.AllowRetest:
		prompt += "（下方按键）"
	}
	fmt.Fprintln(u.out)
	const invalidHint = "无效选择，请按提示输入。"
	invalidShown := false
	for {
		fmt.Fprintf(u.out, "%s：", prompt)
		line, err := u.reader.ReadString('\n')
		if err != nil && len(line) == 0 {
			return "", 0, err
		}
		pasted := u.discardBufferedInput()
		value := strings.TrimSpace(line)
		if value == "" {
			if u.interactive {
				eraseChoiceRetry(u.out, false)
			}
			if pasted && !invalidShown {
				fmt.Fprintln(u.out, "检测到粘贴了多行内容，已忽略多余输入。")
				invalidShown = true
			}
			continue
		}
		action, index, ok := parseSNIPageInput(value, total, page, pages, opts)
		if ok {
			return action, index, nil
		}
		if u.interactive {
			eraseChoiceRetry(u.out, false)
		}
		if pasted {
			fmt.Fprintln(u.out, "检测到粘贴了多行内容，已忽略多余输入。")
			invalidShown = true
			continue
		}
		if invalidShown {
			continue
		}
		fmt.Fprintln(u.out, invalidHint)
		invalidShown = true
	}
}

func parseSNIPageInput(value string, total, page, pages int, opts sniPageOptions) (string, int, bool) {
	switch {
	case strings.EqualFold(value, "q"):
		if opts.AllowBack {
			return sniPageActionBack, 0, true
		}
		if opts.AllowManual {
			return sniPageActionCancel, 0, true
		}
	case value == "0":
		if opts.AllowManual {
			return sniPageActionManual, 0, true
		}
		if opts.AllowBack {
			return sniPageActionBack, 0, true
		}
	case strings.EqualFold(value, "n"):
		if page+1 < pages {
			return sniPageActionNext, 0, true
		}
	case value == "p":
		if page > 0 {
			return sniPageActionPrev, 0, true
		}
	case value == "g":
		if pages > 1 {
			return sniPageActionGoto, 0, true
		}
	case value == "G":
		if pages > 1 {
			return sniPageActionGoto, pages - 1, true
		}
	case opts.AllowRetest && value == "1":
		return sniPageActionRetest, 0, true
	case strings.EqualFold(value, "v"):
		return sniPageActionSortToggle, 0, true
	case strings.EqualFold(value, "v4"):
		return sniPageActionSortIPv4, 0, true
	case strings.EqualFold(value, "v6"):
		return sniPageActionSortIPv6, 0, true
	}
	if len(value) > 1 && (value[0] == 'p' || value[0] == 'P') {
		target, err := strconv.Atoi(value[1:])
		if err == nil && target >= 1 && target <= pages {
			return sniPageActionGoto, target - 1, true
		}
	}
	if !opts.AllowRetest {
		choice, parseErr := strconv.Atoi(value)
		start, end, _ := sniPageBounds(total, page, sniResultPageSize)
		if parseErr == nil && choice >= start+1 && choice <= end {
			return sniPageActionSelect, choice, true
		}
	}
	return "", 0, false
}

func applySNISortAction(sortIPv4 *bool, page *int, action string) bool {
	switch action {
	case sniPageActionSortToggle:
		*sortIPv4 = !*sortIPv4
		*page = 0
		return true
	case sniPageActionSortIPv4:
		*sortIPv4 = true
		*page = 0
		return true
	case sniPageActionSortIPv6:
		*sortIPv4 = false
		*page = 0
		return true
	default:
		return false
	}
}

func sniSortLabel(ipv4 bool) string {
	if ipv4 {
		return "按 IPv4 延迟排序"
	}
	return "按 IPv6 延迟排序"
}

func sniPageBounds(total, page, pageSize int) (start, end, pages int) {
	if pageSize <= 0 {
		pageSize = sniResultPageSize
	}
	if total <= 0 {
		return 0, 0, 1
	}
	pages = (total + pageSize - 1) / pageSize
	if page < 0 {
		page = 0
	}
	if page >= pages {
		page = pages - 1
	}
	start = page * pageSize
	end = start + pageSize
	if end > total {
		end = total
	}
	return start, end, pages
}

func (u *ui) printSNICandidateSummary(index int, candidate sniCandidate, marker string, domainWidth int) {
	_, tlsVersion, alpn, cdn := sniCandidateMetadata(candidate)
	displayMarker := ""
	if marker != "" {
		displayMarker = "  " + marker
	}
	if cdn == "未发现明显特征" {
		cdn = "未识别 CDN"
	} else if cdn == "未知" {
		cdn = "CDN 未知"
	}
	fmt.Fprintf(u.out, "  %2d  %s  %s  ·  %s  %s  ·  %s%s\n",
		index,
		fitDisplay(candidate.Domain, domainWidth),
		formatAlignedFamily("IPv4", candidate.IPv4, candidate.Latency),
		formatAlignedFamily("IPv6", candidate.IPv6, 0),
		padDisplay(fmt.Sprintf("TLS %s / %s", tlsVersion, alpn), 18),
		cdn,
		displayMarker,
	)
}

func (u *ui) printSNICandidateDetails(prefix string, candidate sniCandidate) {
	latency, tlsVersion, alpn, cdn := sniCandidateMetadata(candidate)
	fmt.Fprintf(u.out, "%s%s\n", prefix, candidate.Domain)
	fmt.Fprintf(u.out, "   %s  TLS=%s  ALPN=%s  CDN=%s\n", latency, tlsVersion, alpn, cdn)
	fmt.Fprintf(u.out, "   证书 SAN=%s\n", formatCertificateSANs(candidate.CertificateSANs, 3))
}

func sniCandidateMetadata(candidate sniCandidate) (string, string, string, string) {
	tlsVersion := candidate.TLSVersion
	if tlsVersion == "" {
		tlsVersion = "未知"
	}
	alpn := candidate.ALPN
	if alpn == "" {
		alpn = "未协商"
	}
	cdn := candidate.CDN
	if cdn == "" {
		cdn = "未知"
	}
	return formatSNILatencies(candidate), tlsVersion, alpn, cdn
}

func formatSNILatencies(candidate sniCandidate) string {
	if !candidate.IPv4.Present && !candidate.IPv6.Present {
		return "延迟 " + formatSNIDuration(candidate.Latency)
	}
	return "IPv4 " + formatFamilyLatency(candidate.IPv4) + "  ·  IPv6 " + formatFamilyLatency(candidate.IPv6)
}

func formatFamilyLatency(family familyLatency) string {
	if !family.Present {
		return "无"
	}
	if !family.OK {
		return "失败"
	}
	return formatSNIDuration(family.Latency)
}

func formatAlignedFamily(label string, family familyLatency, fallback time.Duration) string {
	value := formatFamilyLatency(family)
	if !family.Present && fallback > 0 {
		value = formatSNIDuration(fallback)
	}
	return label + " " + padDisplayLeft(value, 5)
}

func formatSNIDuration(latency time.Duration) string {
	rounded := latency.Round(time.Millisecond)
	if rounded < time.Millisecond {
		rounded = time.Millisecond
	}
	return rounded.String()
}

func formatCertificateSANs(sans []string, limit int) string {
	if len(sans) == 0 {
		return "无/未知"
	}
	if limit <= 0 || limit >= len(sans) {
		return strings.Join(sans, ", ")
	}
	return fmt.Sprintf("%s（另有 %d 项）", strings.Join(sans[:limit], ", "), len(sans)-limit)
}

func sniDomainColumnWidth(candidates []sniCandidate) int {
	width := 16
	for _, candidate := range candidates {
		if w := displayWidth(candidate.Domain); w > width {
			width = w
		}
	}
	if width > 36 {
		return 36
	}
	return width
}

func padDisplay(value string, width int) string {
	padding := width - displayWidth(value)
	if padding <= 0 {
		return value
	}
	return value + strings.Repeat(" ", padding)
}

func padDisplayLeft(value string, width int) string {
	padding := width - displayWidth(value)
	if padding <= 0 {
		return value
	}
	return strings.Repeat(" ", padding) + value
}

func fitDisplay(value string, width int) string {
	if displayWidth(value) <= width {
		return padDisplay(value, width)
	}
	var builder strings.Builder
	used := 0
	for _, r := range value {
		w := 1
		if isWideRune(r) {
			w = 2
		}
		if used+w >= width {
			break
		}
		builder.WriteRune(r)
		used += w
	}
	builder.WriteRune('…')
	return padDisplay(builder.String(), width)
}

func (u *ui) drainBufferedNewlines() {
	for u.interactive && u.reader.Buffered() > 0 {
		b, err := u.reader.Peek(1)
		if err != nil || len(b) != 1 || (b[0] != '\n' && b[0] != '\r') {
			return
		}
		_, _ = u.reader.ReadByte()
	}
}
