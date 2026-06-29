package almagest

import (
	"errors"
	"strings"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
)

type spooferConfig struct {
	mtu                  int
	queueLength          int
	disableSACK          bool
	ttl                  int
	icmpBurst            int
	icmpLimit            float64
	tcpSendBufferSize    int
	tcpReceiveBufferSize int
	congestionControlAlg string
	disableNagle         bool
	disableTCPModRecBuff bool
	tcpKeepAlive         bool
	tcpKeepAliveIdle     int
	tcpKeepAliveInterval int
	tcpKeepAliveCount    int
	tcpForwardWnd        int
	tcpForwardAttempts   int
}

func defaultSpooferConfig() spooferConfig {
	return spooferConfig{
		mtu:         defaultSpooferMTU,
		queueLength: defaultSpooferQueue,
	}
}

func (c *spooferConfig) read(cfg configer.Config) {
	if cfg == nil {
		return
	}
	c.mtu = configer.Int(cfg, configer.Path{"mtu"}, c.mtu)
	c.queueLength = configer.Int(cfg, configer.Path{"queueLength"}, c.queueLength)
	c.disableSACK = configer.Bool(cfg, configer.Path{"disableSACK"}, c.disableSACK)
	c.ttl = configer.Int(cfg, configer.Path{"ttl"}, c.ttl)
	c.icmpBurst = configer.Int(cfg, configer.Path{"icmpBurst"}, c.icmpBurst)
	c.icmpLimit = configer.Float(cfg, configer.Path{"icmpLimit"}, c.icmpLimit)
	c.tcpSendBufferSize = configer.Int(cfg, configer.Path{"tcpSendBufferSize"}, c.tcpSendBufferSize)
	c.tcpReceiveBufferSize = configer.Int(cfg, configer.Path{"tcpReceiveBufferSize"}, c.tcpReceiveBufferSize)
	c.congestionControlAlg = configer.String(cfg, configer.Path{"congestionControlAlg"}, c.congestionControlAlg)
	c.disableNagle = configer.Bool(cfg, configer.Path{"disableNagle"}, c.disableNagle)
	c.disableTCPModRecBuff = configer.Bool(cfg, configer.Path{"disableTCPModRecBuff"}, c.disableTCPModRecBuff)
	c.tcpKeepAlive = configer.Bool(cfg, configer.Path{"tcpKeepAlive"}, c.tcpKeepAlive)
	c.tcpKeepAliveIdle = configer.Int(cfg, configer.Path{"tcpKeepAliveIdle"}, c.tcpKeepAliveIdle)
	c.tcpKeepAliveInterval = configer.Int(cfg, configer.Path{"tcpKeepAliveInterval"}, c.tcpKeepAliveInterval)
	c.tcpKeepAliveCount = configer.Int(cfg, configer.Path{"tcpKeepAliveCount"}, c.tcpKeepAliveCount)
	c.tcpForwardWnd = configer.Int(cfg, configer.Path{"tcpForwardWnd"}, c.tcpForwardWnd)
	c.tcpForwardAttempts = configer.Int(cfg, configer.Path{"tcpForwardAttempts"}, c.tcpForwardAttempts)
	c.normalize()
}

func (c *spooferConfig) normalize() {
	if c.mtu <= 0 {
		c.mtu = defaultSpooferMTU
	}
	if c.queueLength <= 0 {
		c.queueLength = defaultSpooferQueue
	}
	c.congestionControlAlg = strings.TrimSpace(c.congestionControlAlg)
}

func validateSpooferConfig(config spooferConfig) error {
	if config.mtu <= 0 {
		return errors.New("MTU must be positive")
	}
	if config.queueLength <= 0 {
		return errors.New("queue length must be positive")
	}
	if config.ttl < 0 || config.ttl > 255 {
		return errors.New("TTL must be between 0 and 255")
	}
	if config.icmpBurst < 0 {
		return errors.New("ICMP burst must not be negative")
	}
	if config.icmpLimit < 0 {
		return errors.New("ICMP limit must not be negative")
	}
	if config.tcpKeepAliveIdle < 0 || config.tcpKeepAliveInterval < 0 || config.tcpKeepAliveCount < 0 {
		return errors.New("TCP keepalive values must not be negative")
	}
	return nil
}

func (n *spooferNode) sendConfigFieldStatus(msg formular.FieldValidateMessage) {
	sendNodeFieldStatus(n.w, n.id, msg.Field, validateSpooferConfigField(msg))
}

func validateSpooferConfigField(msg formular.FieldValidateMessage) error {
	switch msg.Field.FieldID {
	case "mtu":
		value, ok := formular.IntValue(msg.Value)
		if !ok || value <= 0 {
			return errors.New("MTU must be positive")
		}
	case "queueLength":
		value, ok := formular.IntValue(msg.Value)
		if !ok || value <= 0 {
			return errors.New("queue length must be positive")
		}
	case "ttl":
		value, ok := formular.IntValue(msg.Value)
		if !ok || value < 0 || value > 255 {
			return errors.New("TTL must be between 0 and 255")
		}
	case "icmpBurst":
		value, ok := formular.IntValue(msg.Value)
		if !ok || value < 0 {
			return errors.New("ICMP burst must not be negative")
		}
	case "icmpLimit":
		value, ok := formular.FloatValue(msg.Value)
		if !ok || value < 0 {
			return errors.New("ICMP limit must not be negative")
		}
	case "tcpKeepAliveIdle", "tcpKeepAliveInterval", "tcpKeepAliveCount":
		value, ok := formular.IntValue(msg.Value)
		if !ok || value < 0 {
			return errors.New("TCP keepalive values must not be negative")
		}
	case "tcpSendBufferSize", "tcpReceiveBufferSize", "tcpForwardWnd", "tcpForwardAttempts":
		value, ok := formular.IntValue(msg.Value)
		if !ok || value < 0 {
			return errors.New("TCP values must not be negative")
		}
	case "congestionControlAlg":
		return nil
	default:
		return nil
	}
	return nil
}
