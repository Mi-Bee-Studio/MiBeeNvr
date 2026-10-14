package config

import (
	"fmt"
	"strings"
)

// LicenseConfig 商业化收费授权（internal/license）。
//
// 授权文档 = base64(payloadJSON).base64(Ed25519 签名)，由运营侧铸造工具
// （私有运维侧铸造）签发；本机只持公钥验签。文档非机密（签名即完整性）
// 明文落 yaml；required=true 时 P2P 外网链路强制校验授权（过期→优雅断开
// 停拨，续期热生效自动重拨），其余能力不受影响。过期/无效是运行态而非
// 配置错误：启动只校验可配性（配了文档必须配公钥），绝不因授权问题阻塞
// 主服务。
type LicenseConfig struct {
	// Document 是编码后的授权文档（payload.sig）。空 = 未配置。
	Document string `yaml:"document,omitempty"`

	// PublicKey 是 base64 编码的 Ed25519 公钥（32B）。配置了 Document
	// 时必填。
	PublicKey string `yaml:"public_key,omitempty"`

	// Required 为 true 时受控能力（现阶段=P2P 外网链路）未获有效授权
	// 不得放行；false = 能力在场但不强制（授权仅展示）。
	Required bool `yaml:"required"`
}

func (c *LicenseConfig) validate() error {
	if strings.TrimSpace(c.Document) == "" {
		return nil
	}
	if strings.TrimSpace(c.PublicKey) == "" {
		return fmt.Errorf("license.public_key is required when license.document is set")
	}
	return nil
}
