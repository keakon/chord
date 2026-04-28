package main

import (
	"log/slog"
	"strings"

	"github.com/keakon/chord/internal/config"
)

func resolveLogLevel(globalCfg, projectCfg *config.Config) slog.Level {
	levelName := "info"
	if globalCfg != nil && strings.TrimSpace(globalCfg.LogLevel) != "" {
		levelName = strings.TrimSpace(globalCfg.LogLevel)
	}
	if projectCfg != nil && strings.TrimSpace(projectCfg.LogLevel) != "" {
		levelName = strings.TrimSpace(projectCfg.LogLevel)
	}
	switch levelName {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func debugLoggingEnabled(globalCfg, projectCfg *config.Config) bool {
	return resolveLogLevel(globalCfg, projectCfg) <= slog.LevelDebug
}

// logEffectiveProxy logs the effective proxy mode at startup for debugging
// (avoids logging the full URL to prevent leaking credentials).
func logEffectiveProxy(effectiveProxy string) {
	switch {
	case effectiveProxy == "":
		slog.Info("proxy: using environment (HTTP_PROXY/HTTPS_PROXY) or direct if unset")
	case effectiveProxy == "direct":
		slog.Info("proxy: disabled (direct)")
	default:
		slog.Info("proxy: configured", "scheme", proxyScheme(effectiveProxy))
	}
}

func proxyScheme(proxyURL string) string {
	if i := strings.Index(proxyURL, "://"); i >= 0 {
		return proxyURL[:i]
	}
	return "unknown"
}
