package main

import (
	"strings"

	"github.com/keakon/golog"
	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
)

func resolveLogLevel(globalCfg, projectCfg *config.Config) golog.Level {
	levelName := "info"
	if globalCfg != nil && strings.TrimSpace(globalCfg.LogLevel) != "" {
		levelName = strings.TrimSpace(globalCfg.LogLevel)
	}
	if projectCfg != nil && strings.TrimSpace(projectCfg.LogLevel) != "" {
		levelName = strings.TrimSpace(projectCfg.LogLevel)
	}
	switch levelName {
	case "debug":
		return golog.DebugLevel
	case "warn":
		return golog.WarnLevel
	case "error":
		return golog.ErrorLevel
	default:
		return golog.InfoLevel
	}
}

func debugLoggingEnabled(globalCfg, projectCfg *config.Config) bool {
	return resolveLogLevel(globalCfg, projectCfg) <= golog.DebugLevel
}

// logEffectiveProxy logs the effective proxy mode at startup for debugging
// (avoids logging the full URL to prevent leaking credentials).
func logEffectiveProxy(effectiveProxy string) {
	switch {
	case effectiveProxy == "":
		log.Debug("proxy: using environment (HTTP_PROXY/HTTPS_PROXY) or direct if unset")
	case effectiveProxy == "direct":
		log.Debug("proxy: disabled (direct)")
	default:
		log.Debugf("proxy: configured scheme=%v", proxyScheme(effectiveProxy))
	}
}

func proxyScheme(proxyURL string) string {
	if i := strings.Index(proxyURL, "://"); i >= 0 {
		return proxyURL[:i]
	}
	return "unknown"
}
