package tui

import (
	"fmt"
	"strings"

	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/contextdiag"
)

func contextRenderKey(o *contextOverlayState, w, h int, theme string) string {
	return fmt.Sprintf("%v/%d/%t/%t/%s/%s/%s/%d/%d/%d/%s", o.view.Snapshot.ID, o.view.Snapshot.Revision, o.view.Available, o.view.Historical, o.view.AgentName, o.view.UnavailableReason, o.target, o.page, w, h, theme)
}
func contextDiagnosticLines(v agent.ContextDiagnosticsView, page, width int) []string {
	lines := []string{"1 构成    2 剪裁    3 缓存", ""}
	if !v.Available {
		if v.UnavailableReason == "unsupported" {
			lines = append(lines, "当前连接不提供上下文诊断")
		} else {
			lines = append(lines, "尚未观测到此任务派发的请求")
		}
	} else {
		s := v.Snapshot
		title := fmt.Sprintf("最近请求 · %s · %s · %s · %s", v.AgentName, s.ModelRef, s.StartedAt.Local().Format("15:04:05"), contextDiagnosticLabel(s.State))
		if v.Historical {
			title += " · 历史请求"
		}
		lines = append(lines, title, "")
		switch page {
		case 0:
			var total int64
			unknown := 0
			for _, c := range s.Categories {
				total += c.Estimate.Tokens
				unknown += c.Estimate.UnknownItems
			}
			lines = append(lines, fmt.Sprintf("本地输入已估算部分：约 %d tokens（%s）", total, contextDiagnosticLabel(s.Completeness)))
			if s.Usage.State == contextdiag.Missing {
				lines = append(lines, "本次响应尚无用量报告")
			} else {
				lines = append(lines, fmt.Sprintf("本次响应报告的输入：%d tokens（%s）", s.Usage.FullInput, contextDiagnosticLabel(s.Usage.State)))
			}
			if unknown > 0 {
				lines = append(lines, fmt.Sprintf("另有 %d 个附件或其他片段未估算", unknown))
			}
			for _, c := range s.Categories {
				row := fmt.Sprintf("%s：约 %d tokens", contextDiagnosticLabel(c.Kind), c.Estimate.Tokens)
				if c.Estimate.Tokens == 0 && c.Estimate.UnknownItems > 0 {
					row = contextDiagnosticLabel(c.Kind) + "：未估算"
				}
				if total > 0 && c.Estimate.Tokens > 0 {
					row += fmt.Sprintf(" · 占已估算部分 %.1f%%", float64(c.Estimate.Tokens)*100/float64(total))
				}
				if c.Estimate.UnknownItems > 0 {
					row += fmt.Sprintf(" · %d 项未知", c.Estimate.UnknownItems)
				}
				lines = append(lines, "", row)
				for _, source := range c.Sources {
					amount := fmt.Sprintf("约 %d tokens", source.Estimate.Tokens)
					if source.Estimate.UnknownItems > 0 {
						if source.Estimate.Tokens == 0 {
							amount = "未估算"
						}
						amount += fmt.Sprintf(" · %d 项未知", source.Estimate.UnknownItems)
					}
					lines = append(lines, fmt.Sprintf("  %s/%s：%s · %d 片段", contextDiagnosticLabel(source.Kind), source.Label, amount, source.Count))
				}
				if c.Other.Tokens > 0 || c.Other.UnknownItems > 0 {
					lines = append(lines, fmt.Sprintf("  其他：约 %d tokens · %d 项未知", c.Other.Tokens, c.Other.UnknownItems))
				}
			}
			lines = append(lines, "", "范围：协议归一化后的本地输入（provider_input），按 UTF-8 字节数 / 3 估算。适配器及服务端处理可能改变构成；不含协议包装开销。")
		case 1:
			r := s.Reduction
			switch r.State {
			case contextdiag.Unavailable:
				lines = append(lines, "此请求未提供请求级剪裁观测。持久上下文压缩不计入本页。")
			case contextdiag.Disabled:
				lines = append(lines, "请求级剪裁已关闭。")
			default:
				lines = append(lines, fmt.Sprintf("请求剪裁输入面：约 %d → %d tokens", r.Before, r.After))
				if r.Before >= r.After {
					lines = append(lines, fmt.Sprintf("净减少约 %d tokens", r.Before-r.After))
				} else {
					lines = append(lines, fmt.Sprintf("净增加约 %d tokens", r.After-r.Before))
				}
				lines = append(lines, "前后使用同一冻结估算比例；与构成页的估算范围不同。")
			}
			if r.Protected {
				lines = append(lines, "部分结果被保留，以维持有效证据。")
			}
			if r.ReusedStable {
				lines = append(lines, "沿用此前稳定的剪裁输入面。")
			}
			for _, reason := range r.Reasons {
				lines = append(lines, contextDiagnosticLabel(reason))
			}
			for _, rule := range r.Rules {
				lines = append(lines, fmt.Sprintf("%s · %s：%d 项，单规则约减少 %d tokens", rule.Tool, contextDiagnosticLabel(rule.Reason), rule.Count, rule.SavedEstimate))
			}
			if len(r.Rules) > 0 {
				lines = append(lines, "单规则统计不能相加推算净减少量。")
			}
		case 2:
			c := s.Cache
			lines = append(lines, "本地比较："+contextDiagnosticLabel(c.BaselineState))
			if len(c.LocalChanges) > 0 {
				names := make([]string, len(c.LocalChanges))
				for i, k := range c.LocalChanges {
					names[i] = contextDiagnosticLabel(k)
				}
				lines = append(lines, "本地变化："+strings.Join(names, "、"))
			}
			lines = append(lines, "消息前缀："+contextDiagnosticLabel(c.PrefixState))
			if c.ExpectedUsable {
				lines = append(lines, fmt.Sprintf("本地公共消息前缀：约 %d tokens（不保证命中）", c.ExpectedPrefix))
			}
			if s.Usage.CacheRead > 0 {
				lines = append(lines, fmt.Sprintf("服务端报告缓存读取 %d tokens", s.Usage.CacheRead))
			} else if s.Usage.CacheWrite > 0 {
				lines = append(lines, fmt.Sprintf("服务端报告缓存写入 %d tokens，未报告缓存读取", s.Usage.CacheWrite))
			} else {
				lines = append(lines, "未报告可确认的缓存读取，原因未知")
			}
			lines = append(lines, "", "本地变化与服务端缓存观测分别展示，不能解释全部缓存或费用差异。增量传输不代表历史不占上下文。")
		}
		if len(s.Limitations) > 0 {
			lines = append(lines, "", "限制：")
			for _, l := range s.Limitations {
				lines = append(lines, "  "+contextDiagnosticLabel(l))
			}
		}
	}
	var wrapped []string
	for _, line := range lines {
		for part := range strings.SplitSeq(ansi.Hardwrap(line, width, true), "\n") {
			wrapped = append(wrapped, ansi.Truncate(part, width, ""))
		}
	}
	return wrapped
}
func contextDiagnosticLabel(key string) string {
	labels := map[string]string{
		contextdiag.Dispatched: "已发起，等待响应", contextdiag.Completed: "已完成", contextdiag.Failed: "失败", contextdiag.Cancelled: "已取消", contextdiag.Superseded: "已被后续尝试替代", contextdiag.Estimated: "估算", contextdiag.Partial: "部分", contextdiag.Reported: "已报告", contextdiag.Missing: "未报告", contextdiag.Unavailable: "不可用",
		contextdiag.System: "系统提示", contextdiag.Tools: "工具定义", contextdiag.Instructions: "环境与指令", contextdiag.Conversation: "对话", contextdiag.ToolResults: "工具结果", contextdiag.Runtime: "运行提示", contextdiag.Attachments: "附件", contextdiag.Messages: "消息", contextdiag.Tool: "工具", contextdiag.Skill: "技能",
		contextdiag.First: "无本地基线", contextdiag.Comparable: "同域可比较", contextdiag.DomainChanged: "请求域已改变", contextdiag.Same: "相同", contextdiag.Append: "追加", contextdiag.Truncate: "缩短", contextdiag.Rewrite: "改写", contextdiag.Unknown: "未知",
		"adapter_protocol_overhead_unestimated": "未估算适配器及协议包装开销", "upstream_route_unobservable": "上游账户路由不可观测", "projection_incomplete": "输入投影不完整，消息前缀无法比较", "scan_incomplete": "达到采集上限，部分内容未扫描", "sources_incomplete": "来源过多，部分来源合并为其他", "opaque_native": "原生回放内容无法估算", "native_definition_unknown": "服务端工具定义不透明", "schema_unavailable": "工具结构超出估算范围", "custom_provider": "自定义 provider，消息前缀无法比较", "server_context_reuse": "服务端复用上下文，消息前缀无法比较", "request_overrides": "请求覆盖可能改变实际输入", "local_state_in_projection": "输入包含本地状态", "source_unspecified": "部分内容来源未细分", "wrap_up_grace": "收尾阶段保留证据", "below_incremental_min": "新增剪裁收益较小，保留稳定输入面", "no_previous_savings": "此前未观测到剪裁收益",
		"recent_high_risk": "保留近期高风险工具证据", "large_but_unreduced": "部分较大结果仍需完整保留", "frozen_reduced": "沿用已剪裁的稳定结果", "deferred_for_cache": "为保留稳定前缀而暂缓剪裁", "deferred_for_review": "等待下一次证据检查", "recalled_input_protect": "保留已重新取回的证据",
		"repeated_output": "重复输出", "tool_error": "工具错误", "confirmation": "确认结果", "diagnostics": "诊断结果", "diff": "文件差异", "read_like": "读取结果", "search_result": "搜索结果", "numbered_source": "带行号的来源", "json_blob": "结构化结果", "long_log": "长日志", "shell_success": "命令成功输出", "record_listing": "记录列表", "skill_instruction": "技能指令", "generic_stale": "过时输出", "other": "其他",
	}
	if label, ok := labels[key]; ok {
		return label
	}
	return key
}
