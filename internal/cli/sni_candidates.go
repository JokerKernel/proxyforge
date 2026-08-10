package cli

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
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
	fmt.Fprintln(c.out, "REALITY SNI 候选检测")
	fmt.Fprintln(c.out, "----------------------------------------")
	fmt.Fprintf(c.out, "正在并发测试 %d 个 SNI 候选的 DNS、TCP/TLS、ALPN、证书 SAN 和 CDN 特征，请稍候……\n", len(defaultSNICandidates))
	candidates, err := probe(ctx, defaultSNICandidates, server, 10)
	if err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("SNI 测速没有返回可用候选")
	}
	randomIndex := c.randomIndex
	if randomIndex == nil {
		randomIndex = secureRandomIndex
	}
	defaultChoice := randomIndex(len(candidates)) + 1
	fmt.Fprintln(c.out, "当前网络下最快的候选域名（按延迟排序）：")
	fmt.Fprintln(c.out)
	for index, candidate := range candidates {
		c.printSNICandidateSummary(index+1, candidate, index+1 == defaultChoice)
	}
	fmt.Fprintln(c.out, " 0) 手动输入其他域名")
	fmt.Fprintln(c.out)
	fmt.Fprintln(c.out, "提示：以上候选均已通过 DNS、TLS 和证书名称校验；延迟为完整探测耗时。")
	fmt.Fprintln(c.out, "      CDN 为启发式识别，不代表目标归属、长期可用性或使用授权。")
	// 检测期间用户误按的回车会暂存在 bufio.Reader 中；清除空行，避免
	// 检测结束后被当成“选择默认项”。保留任何非空输入，避免吞掉用户已输入的编号。
	for c.interactiveUI() && c.reader.Buffered() > 0 {
		b, peekErr := c.reader.Peek(1)
		if peekErr != nil || len(b) != 1 || (b[0] != '\n' && b[0] != '\r') {
			break
		}
		_, _ = c.reader.ReadByte()
	}
	choice, err := c.chooseNumberCancelable("请选择 SNI（默认从最快 10 个中随机）", 0, len(candidates), defaultChoice)
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

func (c *commandSet) printSNICandidateSummary(index int, candidate app.SNICandidate, selectedByDefault bool) {
	latency, tlsVersion, alpn, cdn := sniCandidateMetadata(candidate)
	defaultMarker := ""
	if selectedByDefault {
		defaultMarker = "  [默认]"
	}
	fmt.Fprintf(c.out, "%2d) %s%s\n", index, candidate.Domain, defaultMarker)
	if cdn == "未发现明显特征" {
		cdn = "未识别 CDN"
	} else if cdn == "未知" {
		cdn = "CDN 未知"
	}
	fmt.Fprintf(c.out, "    延迟 %s  ·  TLS %s / %s  ·  %s\n", latency, tlsVersion, alpn, cdn)
}

func (c *commandSet) printSNICandidateDetails(prefix string, candidate app.SNICandidate) {
	latency, tlsVersion, alpn, cdn := sniCandidateMetadata(candidate)
	fmt.Fprintf(c.out, "%s%s\n", prefix, candidate.Domain)
	fmt.Fprintf(c.out, "   延迟=%s  TLS=%s  ALPN=%s  CDN=%s\n", latency, tlsVersion, alpn, cdn)
	fmt.Fprintf(c.out, "   证书 SAN=%s\n", formatCertificateSANs(candidate.CertificateSANs, 3))
}

func sniCandidateMetadata(candidate app.SNICandidate) (time.Duration, string, string, string) {
	latency := candidate.Latency.Round(time.Millisecond)
	if latency < time.Millisecond {
		latency = time.Millisecond
	}
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
	return latency, tlsVersion, alpn, cdn
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

func secureRandomIndex(size int) int {
	if size <= 1 {
		return 0
	}
	value, err := rand.Int(rand.Reader, big.NewInt(int64(size)))
	if err != nil {
		return int(time.Now().UnixNano() % int64(size))
	}
	return int(value.Int64())
}
