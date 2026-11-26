package app

// 控制接口密钥的首次生成。

import (
	"crypto/rand"
	"encoding/hex"
	"log"

	"fnos-mihomo/internal/settings"
)

// EnsureControllerSecret 首次运行时给控制接口生成一个随机密钥。
//
// 控制口（默认 9090）绑在所有网卡上，密钥为空就等于把内核的控制权
// 和全部节点凭据摊给同一个局域网里的任何人 ——
// 面板密码管的是 8765，管不到这一层。
//
// 用户可以在「基础设置」里显式清空它，清空后行为与从前一致
// （内核不校验密钥，控制口裸奔）。
func EnsureControllerSecret(store *settings.Store) {
	// 只在首次运行时生成：用户后来在「基础设置」里显式清空了密钥，
	// 不该在下次重启时又被塞回去。
	if !store.IsFresh() {
		return
	}
	if store.Get().ControllerSecret != "" {
		return
	}
	buf := make([]byte, 16)
	// Go 1.24 起 crypto/rand.Read 失败会 panic，不会返回错误。
	_, _ = rand.Read(buf)
	secret := hex.EncodeToString(buf)
	if err := store.Update(func(c *settings.Settings) { c.ControllerSecret = secret }); err != nil {
		log.Printf("生成控制密钥失败: %v", err)
	}
}
