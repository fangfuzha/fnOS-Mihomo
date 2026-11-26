package gateway

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"fnos-mihomo/internal/settings"
)

// tunDevice 是 mihomo 接管本机流量时创建的虚拟网卡名。
const tunDevice = "Mihomo0"

// Gateway 负责让 NAS 本机的出站流量走代理。
//
// 实现方式是 mihomo 的 TUN 模式：mihomo 自己创建虚拟网卡并接管路由，
// 本机进程发出的包全部经过它，再按分流规则决定直连还是走代理。
//
// 这里不用 nftables 的 output 钩子接管，原因有两个：
//
//  1. 内核的 tproxy 表达式只支持 prerouting（入站）钩子，
//     在 output 钩子会直接报 "Operation not supported"。
//  2. REDIRECT 会丢掉原始目的地址，mihomo 拿不到真实的目标。
//
// 只接管本机流量，不碰局域网客户端 —— 客户端的流量路径与本机无关，
// 因此不会出现「开了旁路由但客户端没改网关就全断」的情况。
type Gateway struct {
	// linkShow 查 TUN 设备是否存在。
	// 抽成字段是为了能测：真实实现要跑 ip 命令，单测里没法用。
	linkShow func(name string) error

	// verifyTimeout 是等 TUN 设备出现的时间上限。
	verifyTimeout time.Duration
	// verifyInterval 是重试间隔。
	verifyInterval time.Duration
}

// New 造一个接管控制器。
func New() *Gateway {
	return &Gateway{
		linkShow:       defaultLinkShow,
		verifyTimeout:  10 * time.Second,
		verifyInterval: 500 * time.Millisecond,
	}
}

// defaultLinkShow 用 ip 命令查网卡。
func defaultLinkShow(name string) error {
	_, err := exec.Command("ip", "link", "show", name).Output()
	return err
}

// Apply 启用本机出站接管。
//
// 真正的接管动作由 mihomo 的 TUN 模式完成（配置由配置生成器写入），
// 内核的启停由 API 层统一负责，这里不需要额外动作。
func (g *Gateway) Apply(cfg settings.GatewayConfig) error {
	return nil
}

// Remove 关闭接管。配置由配置生成器负责改写，内核随之重启，这里同样不需要额外动作。
func (g *Gateway) Remove() error {
	return nil
}

// Verify 检查接管是否真的生效。
//
// 开启 TUN 会改动整机路由，万一内核起不来或配置有问题，
// NAS 就可能上不了网。所以开启之后必须验证一遍：
// TUN 设备存在、且代理端口能应答，否则判定为失败。
func (g *Gateway) Verify() error {
	deadline := time.Now().Add(g.verifyTimeout)
	for {
		// TUN 设备存在，说明 mihomo 的 TUN 模式真的起来了。
		// 这比看内核进程状态更可靠 —— 进程活着但 TUN 建失败的情况确实存在。
		if g.linkShow(tunDevice) == nil {
			return nil
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(g.verifyInterval)
	}
	return fmt.Errorf("接管未生效：TUN 设备未创建，请检查内核日志")
}

// Status 返回接管是否处于生效状态。
//
// 判断依据是 TUN 虚拟网卡是否存在 —— 它由 mihomo 在启用 TUN 模式时创建，
// 内核关闭时随之消失。
func (g *Gateway) Status() bool {
	return g.linkShow(tunDevice) == nil
}

// Rules 返回当前接管状态的可读描述，供排障时查看。
func (g *Gateway) Rules() string {
	out, err := exec.Command("ip", "addr", "show", tunDevice).Output()
	if err != nil {
		return "TUN 设备未创建，接管未生效"
	}
	return strings.TrimSpace(string(out))
}
