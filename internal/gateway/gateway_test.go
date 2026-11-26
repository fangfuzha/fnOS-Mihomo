package gateway

import (
	"errors"
	"strings"
	"testing"
	"time"

	"fnos-mihomo/internal/settings"
)

// newTestGateway 造一个假的接管控制器：TUN 设备存在与否由 exists 决定。
func newTestGateway(exists bool) *Gateway {
	g := New()
	g.linkShow = func(name string) error {
		if exists {
			return nil
		}
		return errors.New("no such device")
	}
	// 单测里等 10 秒没必要，把超时压到毫秒级。
	g.verifyTimeout = 20 * time.Millisecond
	g.verifyInterval = time.Millisecond
	return g
}

func TestVerifySucceedsWhenTunDeviceExists(t *testing.T) {
	g := newTestGateway(true)
	if err := g.Verify(); err != nil {
		t.Fatalf("TUN 设备存在时 Verify 应该成功: %v", err)
	}
}

// TUN 没建起来时必须报错：接管会改整机路由，
// 带着「接管失败」的状态放过去，NAS 可能就上不了网了。
func TestVerifyFailsWhenTunDeviceMissing(t *testing.T) {
	g := newTestGateway(false)
	err := g.Verify()
	if err == nil {
		t.Fatal("TUN 设备不存在时 Verify 应该失败")
	}
	if !strings.Contains(err.Error(), "TUN") {
		t.Fatalf("错误信息应该说明 TUN 没建起来: %v", err)
	}
}

func TestStatusFollowsTunDevice(t *testing.T) {
	if !newTestGateway(true).Status() {
		t.Fatal("TUN 设备存在时 Status 应该为 true")
	}
	if newTestGateway(false).Status() {
		t.Fatal("TUN 设备不存在时 Status 应该为 false")
	}
}

// 接管本身由 mihomo 的 TUN 模式完成，Apply/Remove 只是接口占位，
// 任何时候都不该报错。
func TestApplyAndRemoveDoNotFail(t *testing.T) {
	g := newTestGateway(true)
	if err := g.Apply(settings.GatewayConfig{Enabled: true}); err != nil {
		t.Fatalf("Apply 不该报错: %v", err)
	}
	if err := g.Remove(); err != nil {
		t.Fatalf("Remove 不该报错: %v", err)
	}
}
