package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"proxyforge/internal/app"
)

var defaultSNICandidates = []string{
	"www.bing.com",
	"th.bing.com",
	"ts1.tc.mm.bing.net",
	"ts3.tc.mm.bing.net",
	"ts4.tc.mm.bing.net",
	"r.bing.com",
	"www.xbox.com",
	"assets-www.xbox.com",
	"assets-xbxweb.xbox.com",
	"sisu.xboxlive.com",
	"azure.microsoft.com",
	"cdn-dynmedia-1.microsoft.com",
	"cdn77.api.userway.org",
	"c.s-microsoft.com",
	"acctcdn.msftauth.net",
	"aadcdn.msftauth.net",
	"configuration.ls.apple.com",
	"devblogs.microsoft.com",
	"img-prod-cms-rt-microsoft-com.akamaized.net",
	"aws.com",
	"aws.amazon.com",
	"www.aws.com",
	"s0.awsstatic.com",
	"d1.awsstatic.com",
	"t0.m.awsstatic.com",
	"a0.awsstatic.com",
	"prod.pa.cdn.uis.awsstatic.com",
	"a.b.cdn.console.awsstatic.com",
	"vs.aws.amazon.com",
	"prod.us-east-1.ui.gcr-chat.marketing.aws.dev",
	"amd.com",
	"www.amd.com",
	"download.amd.com",
	"drivers.amd.com",
	"intel.com",
	"downloadmirror.intel.com",
	"images.nvidia.com",
	"www.nvidia.com",
	"iosapps.itunes.apple.com",
	"se-edge.itunes.apple.com",
	"is1-ssl.mzstatic.com",
	"apps.mzstatic.com",
	"gsp-ssl.ls.apple.com",
	"www.sony.com",
	"electronics.sony.com",
	"digitalassets.tesla.com",
	"cua-chat-ui.tesla.com",
	"www.tesla.com",
	"catalog.gamepass.com",
	"assets.adobedtm.com",
	"cdn.bizibly.com",
	"tag-logger.demandbase.com",
	"tag.demandbase.com",
	"mscom.demdex.net",
	"logx.optimizely.com",
	"cdnssl.clicktale.net",
	"s.go-mpulse.net",
	"s.company-target.com",
	"beacon.gtv-pub.com",
	"snap.licdn.com",
	"j.6sc.co",
	"c.6sc.co",
	"b.6sc.co",
	"static.cloud.coveo.com",
	"ce.mf.marsflag.com",
	"s.mp.marsflag.com",
	"gray-config-prod.api.arc-cdn.net",
	"gray-wowt-prod.gtv-cdn.com",
	"www.wowt.com",
	"cdn.userway.org",
	"github.gallerycdn.vsassets.io",
	"ms-vscode.gallerycdn.vsassets.io",
	"ms-python.gallerycdn.vsassets.io",
}

type sniCandidateProbeFunc func(context.Context, []string, string, int) ([]app.SNICandidate, error)

func (c *commandSet) selectSNICandidate(ctx context.Context, server string) (string, error) {
	probe := c.probeSNI
	if probe == nil {
		probe = app.ProbeSNICandidates
	}
	c.clearScreen()
	c.printPageHeader("REALITY SNI 候选检测")
	fmt.Fprintf(c.out, "正在并发测试 %d 个 SNI 候选的 DNS、TCP/TLS、ALPN、证书 SAN 和 CDN 特征，请稍候……\n", len(defaultSNICandidates))
	candidates, err := probe(ctx, defaultSNICandidates, server, app.DefaultSNICandidateLimit)
	if err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("SNI 测速没有返回可用候选")
	}
	fmt.Fprintln(c.out, "当前网络下最快的候选域名（按较快地址族延迟排序）：")
	fmt.Fprintln(c.out)
	for index, candidate := range candidates {
		c.printSNICandidateSummary(index+1, candidate, "")
	}
	c.printMenuChoice("0", "手动输入")
	fmt.Fprintln(c.out)
	fmt.Fprintln(c.out, "提示：以上候选均已通过 DNS、TLS 和证书名称校验；IPv4/IPv6 延迟为各自 TCP+TLS 探测耗时。")
	fmt.Fprintln(c.out, "      CDN 为启发式识别，不代表目标归属、长期可用性或使用授权。")
	// 检测期间用户误按的回车会暂存在 bufio.Reader 中；清除空行，避免
	// 检测结束后立即触发一次无效选择。保留任何非空输入，避免吞掉用户已输入的编号。
	for c.interactiveUI() && c.reader.Buffered() > 0 {
		b, peekErr := c.reader.Peek(1)
		if peekErr != nil || len(b) != 1 || (b[0] != '\n' && b[0] != '\r') {
			break
		}
		_, _ = c.reader.ReadByte()
	}
	choice, err := c.chooseNumberCancelable("请选择 SNI（必须输入编号，0 表示手动输入）", 0, len(candidates), -1)
	if err != nil {
		return "", err
	}
	if choice > 0 {
		return candidates[choice-1].Domain, nil
	}
	for {
		domain, err := c.askDefaultCancelable("请输入 REALITY SNI", "")
		if err != nil {
			return "", err
		}
		domain = strings.TrimSpace(domain)
		if domain != "" {
			if err := c.confirmManualSNI(ctx, domain, server); err != nil {
				return "", err
			}
			return domain, nil
		}
		fmt.Fprintln(c.out, "SNI 不能为空。")
	}
}

func (c *commandSet) retestSNICandidates(ctx context.Context, core string) error {
	current, err := c.app.Store.Load(core)
	if err != nil {
		return err
	}
	currentSNI := strings.TrimSpace(current.SNI)
	server := strings.TrimSpace(current.Server)
	if currentSNI == "" || server == "" {
		return fmt.Errorf("当前受管节点缺少 Server 或 REALITY SNI，无法重新检测")
	}

	probe := c.probeSNI
	if probe == nil {
		probe = app.ProbeSNICandidates
	}
	probeDomains := make([]string, 0, len(defaultSNICandidates)+1)
	probeDomains = append(probeDomains, currentSNI)
	probeDomains = append(probeDomains, defaultSNICandidates...)

	for {
		c.clearScreen()
		c.printPageHeader(core, "REALITY SNI 候选重新检测")
		fmt.Fprintf(c.out, "节点地址：%s\n", server)
		fmt.Fprintf(c.out, "当前 SNI：%s\n\n", currentSNI)
		fmt.Fprintf(c.out, "正在并发重新测试当前 SNI 和 %d 个内置候选，请稍候……\n", len(defaultSNICandidates))
		results, err := probe(ctx, probeDomains, server, len(probeDomains))
		if err != nil {
			return fmt.Errorf("重新检测 REALITY SNI 候选: %w", err)
		}

		currentRank := 0
		var currentResult app.SNICandidate
		for index, candidate := range results {
			if strings.EqualFold(candidate.Domain, currentSNI) {
				currentRank = index + 1
				currentResult = candidate
				break
			}
		}
		if currentRank == 0 {
			fmt.Fprintln(c.out, "\n[警告] 当前 SNI 未通过本次 DNS、TLS 或证书名称校验。")
			fmt.Fprintln(c.out, "检测结果可能受临时网络波动影响，可立即重新测试；本次不会修改配置。")
		} else {
			fmt.Fprintf(c.out, "\n[结果] 当前 SNI 通过检测，在全部有效候选中排名第 %d。\n", currentRank)
			c.printSNICandidateDetails("当前 SNI：", currentResult)
		}

		displayLimit := app.DefaultSNICandidateLimit
		if len(results) < displayLimit {
			displayLimit = len(results)
		}
		fmt.Fprintf(c.out, "\n当前网络下最快的 %d 个候选域名：\n\n", displayLimit)
		for index, candidate := range results[:displayLimit] {
			marker := ""
			if strings.EqualFold(candidate.Domain, currentSNI) {
				marker = "[当前 SNI]"
			}
			c.printSNICandidateSummary(index+1, candidate, marker)
		}
		fmt.Fprintln(c.out, "\n提示：本操作只重新检测和展示结果，不会修改 SNI、target 或重启服务。")
		fmt.Fprintln(c.out, "      如需采用新候选，请返回后选择“重置 SNI/target”。")
		fmt.Fprintln(c.out)
		c.printMenuChoice("1", "重新测试")
		c.printMenuChoice("0/q", "返回服务端配置")
		choice, err := c.chooseNumber("请选择", 0, 1, 0)
		if err != nil {
			return err
		}
		if choice == 0 {
			return nil
		}
	}
}

func (c *commandSet) confirmManualSNI(ctx context.Context, domain, server string) error {
	probe := c.probeSNI
	if probe == nil {
		probe = app.ProbeSNICandidates
	}
	fmt.Fprintf(c.out, "正在检测手动 SNI %s 的 DNS、TCP/TLS、ALPN、证书 SAN 和 CDN 特征，请稍候……\n", domain)
	candidates, err := probe(ctx, []string{domain}, server, 1)
	if err != nil {
		return fmt.Errorf("手动 SNI %s 未通过检测: %w", domain, err)
	}
	if len(candidates) != 1 {
		return fmt.Errorf("手动 SNI %s 未返回有效检测结果", domain)
	}
	fmt.Fprintln(c.out, "手动 SNI 检测结果：")
	c.printSNICandidateDetails("域名：", candidates[0])
	fmt.Fprintln(c.out, "提示：检测结果只反映当前网络状态，不代表目标归属、长期可用性或使用授权。")
	ok, err := c.confirmCancelable("确认采用这个手动 SNI？")
	if err != nil {
		return err
	}
	if !ok {
		return errReturnToMenu
	}
	return nil
}

func (c *commandSet) printSNICandidateSummary(index int, candidate app.SNICandidate, marker string) {
	latency, tlsVersion, alpn, cdn := sniCandidateMetadata(candidate)
	displayMarker := ""
	if marker != "" {
		displayMarker = "  " + marker
	}
	fmt.Fprintf(c.out, "  %d %s%s\n", index, candidate.Domain, displayMarker)
	if cdn == "未发现明显特征" {
		cdn = "未识别 CDN"
	} else if cdn == "未知" {
		cdn = "CDN 未知"
	}
	fmt.Fprintf(c.out, "    %s  ·  TLS %s / %s  ·  %s\n", latency, tlsVersion, alpn, cdn)
}

func (c *commandSet) printSNICandidateDetails(prefix string, candidate app.SNICandidate) {
	latency, tlsVersion, alpn, cdn := sniCandidateMetadata(candidate)
	fmt.Fprintf(c.out, "%s%s\n", prefix, candidate.Domain)
	fmt.Fprintf(c.out, "   %s  TLS=%s  ALPN=%s  CDN=%s\n", latency, tlsVersion, alpn, cdn)
	fmt.Fprintf(c.out, "   证书 SAN=%s\n", formatCertificateSANs(candidate.CertificateSANs, 3))
}

func sniCandidateMetadata(candidate app.SNICandidate) (string, string, string, string) {
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

func formatSNILatencies(candidate app.SNICandidate) string {
	if !candidate.IPv4.Present && !candidate.IPv6.Present {
		return "延迟 " + formatSNIDuration(candidate.Latency)
	}
	return "IPv4 " + formatFamilyLatency(candidate.IPv4) + "  ·  IPv6 " + formatFamilyLatency(candidate.IPv6)
}

func formatFamilyLatency(family app.FamilyLatency) string {
	if !family.Present {
		return "无"
	}
	if !family.OK {
		return "失败"
	}
	return formatSNIDuration(family.Latency)
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
