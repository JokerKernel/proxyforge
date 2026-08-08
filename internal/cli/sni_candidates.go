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
	fmt.Fprintf(c.out, "正在并发测试 %d 个 SNI 候选的 DNS、TCP/TLS 和证书，请稍候……\n", len(defaultSNICandidates))
	candidates, err := probe(ctx, defaultSNICandidates, server, 10)
	if err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("SNI 测速没有返回可用候选")
	}
	fmt.Fprintln(c.out, "当前网络下最快的候选域名：")
	for index, candidate := range candidates {
		latency := candidate.Latency.Round(time.Millisecond)
		if latency < time.Millisecond {
			latency = time.Millisecond
		}
		fmt.Fprintf(c.out, "%d) %-48s %s\n", index+1, candidate.Domain, latency)
	}
	fmt.Fprintln(c.out, "0) 手动输入其他域名")
	fmt.Fprintln(c.out, "提示：测速只代表当前 TLS 握手速度，不代表目标归属、长期可用性或使用授权。")
	randomIndex := c.randomIndex
	if randomIndex == nil {
		randomIndex = secureRandomIndex
	}
	defaultChoice := randomIndex(len(candidates)) + 1
	choice, err := c.chooseNumber("请选择 SNI（默认从最快 10 个中随机）", 0, len(candidates), defaultChoice)
	if err != nil {
		return "", err
	}
	if choice > 0 {
		return candidates[choice-1].Domain, nil
	}
	for {
		domain := strings.TrimSpace(c.askDefault("请输入 REALITY SNI", ""))
		if domain != "" {
			return domain, nil
		}
		fmt.Fprintln(c.out, "SNI 不能为空。")
	}
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
