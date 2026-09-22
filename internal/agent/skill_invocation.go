package agent

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/tools"
)

// userSkillCallSeq numbers synthesized skill tool calls. The prefix keeps them
// apart from provider-issued IDs, so a restored transcript never reads one as
// the other. The process token keeps them apart across restarts: a resumed
// session that loads another skill must not reuse the ID the previous process
// persisted for a different skill.
var userSkillCallSeq atomic.Uint64

var (
	userSkillProcToken     string
	userSkillProcTokenOnce sync.Once
)

func userSkillProcessToken() string {
	userSkillProcTokenOnce.Do(func() {
		var buf [4]byte
		if _, err := rand.Read(buf[:]); err == nil {
			userSkillProcToken = hex.EncodeToString(buf[:])
			return
		}
		userSkillProcToken = fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	})
	return userSkillProcToken
}

// errSkillLoadFailed marks a skill body that could not be read from disk, and
// errSkillEncodeArgs marks arguments that could not be marshaled for the
// synthesized call. Both surface as error-level toasts; any other /skill
// failure (unknown name, ruleset denial) stays a warning.
var (
	errSkillLoadFailed = errors.New("failed to load skill")
	errSkillEncodeArgs = errors.New("encode skill arguments")
)

// skillLoadToastLevel maps a /skill failure to its toast severity without
// coupling to the rendered message text.
func skillLoadToastLevel(err error) string {
	if errors.Is(err, errSkillLoadFailed) || errors.Is(err, errSkillEncodeArgs) {
		return "error"
	}
	return "warn"
}

func nextUserSkillCallID() string {
	return fmt.Sprintf("user-skill-%s-%d", userSkillProcessToken(), userSkillCallSeq.Add(1))
}

// resolveAndBuildUserSkillPair is the shared parse, authorization and pair
// construction for an explicit /skill load. MainAgent and SubAgent differ only
// in how they persist the pair and surface toasts, so they share this pure
// step and keep their own write paths.
func resolveAndBuildUserSkillPair(states []skill.InvocationState, content string) (sk *skill.Skill, assistantMsg, toolMsg message.Message, callName string, loadErr error) {
	name, args, ok := parseUserSkillCommand(content)
	if !ok {
		return nil, message.Message{}, message.Message{}, "", nil
	}
	state, err := resolveUserSkillInvocation(states, name)
	if err != nil {
		return nil, message.Message{}, message.Message{}, name, err
	}
	loaded, err := skill.LoadSkill(state.Meta.Location)
	if err != nil {
		return nil, message.Message{}, message.Message{}, name, fmt.Errorf("%w %q: %w", errSkillLoadFailed, name, err)
	}
	assistantMsg, toolMsg, err = buildUserSkillInvocationPair(loaded, args, nextUserSkillCallID())
	if err != nil {
		return nil, message.Message{}, message.Message{}, name, err
	}
	return loaded, assistantMsg, toolMsg, name, nil
}

// parseUserSkillCommand parses `/skill <name> [args]`. Bare `/skill` is not an
// invocation: the TUI turns it into the selector, so a name is required here
// and the rest of the line is passed through as the skill's args verbatim.
func parseUserSkillCommand(content string) (name, args string, ok bool) {
	c := strings.TrimSpace(content)
	if !strings.HasPrefix(c, "/skill") {
		return "", "", false
	}
	rest := c[len("/skill"):]
	if rest == "" {
		return "", "", false // bare /skill opens the selector
	}
	switch rest[0] {
	case ' ', '\t', '\n', '\r':
	default:
		return "", "", false // /skillx is not the command
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", "", false
	}
	if idx := strings.IndexAny(rest, " \t\n\r"); idx >= 0 {
		return rest[:idx], strings.TrimSpace(rest[idx+1:]), true
	}
	return rest, "", true
}

// resolveUserSkillInvocation finds a skill the user may load by name. It is the
// single authorization gate for the explicit /skill path, and it reads the same
// InvocationState the selector renders, so a row shown as loadable always loads
// here and a row shown as denied is refused with an explicit reason.
func resolveUserSkillInvocation(states []skill.InvocationState, name string) (skill.InvocationState, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return skill.InvocationState{}, fmt.Errorf("skill name is required")
	}
	for _, st := range states {
		if st.Meta == nil || st.Meta.Name != name {
			continue
		}
		if !st.UserLoadable {
			return skill.InvocationState{Reason: skill.ReasonDeniedByRuleset}, fmt.Errorf("skill %q is not available to this agent", name)
		}
		return st, nil
	}
	return skill.InvocationState{Reason: skill.ReasonNotFound}, fmt.Errorf("skill %q not found", name)
}

// buildUserSkillInvocationPair builds the assistant tool call and its paired
// tool result for an explicit skill load. The shape mirrors a model-produced
// skill call exactly — same argument marshaling, same result formatter, success
// status — so restore, durable compaction and the TUI treat both origins the
// same. Only Provenance.Origin tells a user-triggered load apart from a model
// call that produced the identical shape.
func buildUserSkillInvocationPair(sk *skill.Skill, args, callID string) (message.Message, message.Message, error) {
	if sk == nil {
		return message.Message{}, message.Message{}, fmt.Errorf("skill is nil")
	}
	rawArgs, err := tools.SkillCallArguments(sk.Name, args)
	if err != nil {
		return message.Message{}, message.Message{}, fmt.Errorf("%w: %w", errSkillEncodeArgs, err)
	}
	assistantMsg := message.Message{
		Role:       message.RoleAssistant,
		ToolCalls:  []message.ToolCall{{ID: callID, Name: tools.NameSkill, Args: rawArgs}},
		Provenance: &message.MessageProvenance{Origin: message.OriginUser},
	}
	toolMsg := message.Message{
		Role:       message.RoleTool,
		ToolCallID: callID,
		Content:    tools.FormatSkillInvocationResult(sk, args),
		ToolStatus: message.ToolStatusSuccess,
		Provenance: &message.MessageProvenance{Origin: message.OriginUser},
	}
	return assistantMsg, toolMsg, nil
}

// appendUserSkillInvocation appends the synthesized skill pair for a user
// message whose content is `/skill <name> [args]`. It returns the appended
// messages (nil when the message is not a skill command or the load failed) so
// a caller that also builds a request slice can carry them along. A failed load
// only toasts: the user's original message stays committed either way.
func (a *MainAgent) appendUserSkillInvocation(userMsg message.Message) []message.Message {
	sk, assistantMsg, toolMsg, _, err := resolveAndBuildUserSkillPair(a.skillInvocationStates(), userMsg.Content)
	if sk == nil || err != nil {
		if err != nil {
			a.emitToTUI(ToastEvent{Message: err.Error(), Level: skillLoadToastLevel(err)})
		}
		return nil
	}
	pair := []message.Message{assistantMsg, toolMsg}
	for _, m := range pair {
		a.ctxMgr.Append(m)
		a.recordEvidenceFromMessage(m)
		if a.recoveryManager() != nil {
			a.persistAsync(identity.MainAgentID, m)
		}
	}
	a.MarkSkillInvokedByName(sk.Name)
	return pair
}

// appendUserSkillInvocation is the worker counterpart of the MainAgent method:
// same parse, authorization and pair shape, but the messages persist through
// the SubAgent's own write path.
func (s *SubAgent) appendUserSkillInvocation(userMsg message.Message) {
	sk, assistantMsg, toolMsg, _, err := resolveAndBuildUserSkillPair(s.skillInvocationStates(), userMsg.Content)
	if sk == nil || err != nil {
		if err != nil {
			s.emitToast(err.Error(), skillLoadToastLevel(err))
		}
		return
	}
	for _, m := range []message.Message{assistantMsg, toolMsg} {
		s.ctxMgr.Append(m)
		s.persistMessageAsync(m, "user skill invocation", nil)
	}
	s.MarkSkillInvoked(&sk.Meta)
}

func (s *SubAgent) emitToast(text, level string) {
	if s == nil || s.parent == nil {
		return
	}
	s.parent.emitToTUI(ToastEvent{Message: text, Level: level, AgentID: s.instanceID})
}
