package acpmux

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/keakon/chord/internal/acpagent"
)

// harness wires a Proxy to an in-process ACP client through pipes, and hands
// every session a fake child instead of a real process. The fake child is the
// SDK's agent side over two pipes: the frontend cannot tell it from a spawned
// `chord acp --session-child`.
type harness struct {
	t      *testing.T
	proxy  *Proxy
	client *recordingClient
	conn   *acp.ClientSideConnection

	mu        sync.Mutex
	spawned   []spawnCall
	agents    []*stubAgent
	children  []*fakeChild
	spawnErr  error
	spawnGate chan struct{}
	spawnSeen chan struct{}
	childInit func(*stubAgent)
	// childExitGate, when set, is installed on each new fake child so its exit
	// can be held open while a test observes the session table.
	childExitGate chan struct{}
	spawnedNo     int
}

type spawnCall struct {
	cwd string
	id  string
}

func newHarness(t *testing.T, opts Options) *harness {
	t.Helper()
	h := &harness{t: t, spawnSeen: make(chan struct{}, 64)}
	clientRead, agentWrite := io.Pipe()
	agentRead, clientWrite := io.Pipe()

	opts.Spawn = h.spawn
	h.proxy = New(opts)
	h.proxy.Bind(acp.NewAgentSideConnection(h.proxy, agentWrite, agentRead))
	h.client = &recordingClient{}
	h.conn = acp.NewClientSideConnection(h.client, clientWrite, clientRead)

	t.Cleanup(func() {
		h.proxy.Shutdown(5 * time.Second)
		_ = agentWrite.Close()
		_ = agentRead.Close()
		_ = clientWrite.Close()
		_ = clientRead.Close()
	})
	return h
}

func (h *harness) spawn(cwd, id string) (Child, error) {
	h.mu.Lock()
	err := h.spawnErr
	gate := h.spawnGate
	initChild := h.childInit
	exitGate := h.childExitGate
	h.spawnedNo++
	ordinal := h.spawnedNo
	h.mu.Unlock()
	h.spawnSeen <- struct{}{}
	if gate != nil {
		<-gate
	}
	if err != nil {
		return nil, err
	}
	agent := newStubAgent(fmt.Sprintf("agent-%d", ordinal))
	if initChild != nil {
		initChild(agent)
	}
	child := newFakeChild(agent)
	if exitGate != nil {
		child.mu.Lock()
		child.exitGate = exitGate
		child.mu.Unlock()
	}
	h.mu.Lock()
	h.spawned = append(h.spawned, spawnCall{cwd: cwd, id: id})
	h.agents = append(h.agents, agent)
	h.children = append(h.children, child)
	h.mu.Unlock()
	return child, nil
}

// blockSpawn makes the next spawn wait until gate is closed, so a test can hold
// a session in its reserved state.
func (h *harness) blockSpawn(gate chan struct{}) {
	h.mu.Lock()
	h.spawnGate = gate
	h.mu.Unlock()
}

// blockChildExit makes spawned children finish only after gate is closed, so a
// test can keep a dying child in the session table.
func (h *harness) blockChildExit(gate chan struct{}) {
	h.mu.Lock()
	h.childExitGate = gate
	h.mu.Unlock()
}

// setChildInit configures each stub agent before it serves any traffic.
func (h *harness) setChildInit(fn func(*stubAgent)) {
	h.mu.Lock()
	h.childInit = fn
	h.mu.Unlock()
}

func (h *harness) calls() []spawnCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]spawnCall(nil), h.spawned...)
}

func (h *harness) agent(i int) *stubAgent {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.agents[i]
}

// agentIfAny is agent without the panic, for a poll that runs before the child
// exists.
func (h *harness) agentIfAny(i int) *stubAgent {
	h.mu.Lock()
	defer h.mu.Unlock()
	if i >= len(h.agents) {
		return nil
	}
	return h.agents[i]
}

func (h *harness) child(i int) *fakeChild {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.children[i]
}

func (h *harness) childCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.children)
}

func (h *harness) failSpawn(err error) {
	h.mu.Lock()
	h.spawnErr = err
	h.mu.Unlock()
}

func newTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func textPrompt(session acp.SessionId, text string) acp.PromptRequest {
	return acp.PromptRequest{SessionId: session, Prompt: []acp.ContentBlock{acp.TextBlock(text)}}
}

// newSessionRequest builds a session/new the client side accepts: the SDK
// requires mcpServers to be present, even when the client starts none.
func newSessionRequest(cwd string) acp.NewSessionRequest {
	return acp.NewSessionRequest{Cwd: cwd, McpServers: []acp.McpServer{}}
}

// waitFor polls a condition the frontend reaches asynchronously, such as a
// reaped child. It fails the test instead of hanging it.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// fakeChild is one in-process session child: the agent side of a pipe pair.
type fakeChild struct {
	agent    *stubAgent
	reader   *io.PipeReader
	writer   *io.PipeWriter
	childOut *io.PipeWriter
	childIn  *io.PipeReader

	mu       sync.Mutex
	stops    int
	closes   int
	waitErr  error
	exitOnce sync.Once
	exited   chan struct{}
	// exitGate, when set, holds finish until it is closed so a test can keep a
	// dying child in the session table long enough to observe the limit.
	exitGate chan struct{}
}

func newFakeChild(agent *stubAgent) *fakeChild {
	childIn, proxyWrite := io.Pipe()
	proxyRead, childOut := io.Pipe()
	c := &fakeChild{
		agent:    agent,
		reader:   proxyRead,
		writer:   proxyWrite,
		childOut: childOut,
		childIn:  childIn,
		exited:   make(chan struct{}),
	}
	agent.conn = acp.NewAgentSideConnection(agent, childOut, childIn)
	go func() {
		<-agent.conn.Done()
		// A real child process closes its fds when it exits, which is what
		// gives the frontend EOF on the child's output.
		_ = childOut.Close()
		c.finish(nil)
	}()
	return c
}

func (c *fakeChild) Reader() io.Reader      { return c.reader }
func (c *fakeChild) Writer() io.WriteCloser { return c.writer }

func (c *fakeChild) Wait() error {
	<-c.exited
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.waitErr
}

func (c *fakeChild) Stop(grace time.Duration) error {
	c.mu.Lock()
	c.stops++
	c.mu.Unlock()
	// Closing the frontend's end of the child's input is what a real child sees
	// as EOF on stdin, and what makes it exit.
	_ = c.writer.Close()
	select {
	case <-c.exited:
		return nil
	case <-time.After(grace):
		return errors.New("fake child did not exit")
	}
}

func (c *fakeChild) stopCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stops
}

// Close releases the fake child's pipes, the way a real process releases its
// fds on exit. The frontend's ends are the ones it holds, so a pipe it never
// released is what a leak would show up as.
func (c *fakeChild) Close() error {
	c.mu.Lock()
	c.closes++
	c.mu.Unlock()
	_ = c.reader.Close()
	_ = c.writer.Close()
	return nil
}

func (c *fakeChild) closeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closes
}

func (c *fakeChild) exitedNow() bool {
	select {
	case <-c.exited:
		return true
	default:
		return false
	}
}

func (c *fakeChild) finish(err error) {
	c.exitOnce.Do(func() {
		c.mu.Lock()
		gate := c.exitGate
		c.mu.Unlock()
		if gate != nil {
			<-gate
		}
		c.mu.Lock()
		if err != nil {
			c.waitErr = err
		}
		c.mu.Unlock()
		close(c.exited)
	})
}

// crash makes the child die mid-session: its output ends without a clean
// shutdown, exactly like a killed process.
func (c *fakeChild) crash(err error) {
	c.mu.Lock()
	c.waitErr = err
	c.mu.Unlock()
	_ = c.childOut.Close()
	c.finish(nil)
}

// stubAgent is the agent behind a fake child: one session, in memory.
type stubAgent struct {
	name    string
	mu      sync.Mutex
	conn    *acp.AgentSideConnection
	init    []acp.InitializeRequest
	news    []acp.NewSessionRequest
	prompts []acp.PromptRequest
	cancels []acp.CancelNotification
	closes  []acp.CloseSessionRequest

	nextSession int
	onNew       func(acp.NewSessionRequest) (acp.NewSessionResponse, error)
	onPrompt    func(context.Context, acp.PromptRequest) (acp.PromptResponse, error)
	onClose     func(acp.CloseSessionRequest) (acp.CloseSessionResponse, error)
}

var _ acp.Agent = (*stubAgent)(nil)

func newStubAgent(name string) *stubAgent { return &stubAgent{name: name} }

func (a *stubAgent) Initialize(_ context.Context, req acp.InitializeRequest) (acp.InitializeResponse, error) {
	a.mu.Lock()
	a.init = append(a.init, req)
	a.mu.Unlock()
	return acpagent.InitializeResponse("stub"), nil
}

func (a *stubAgent) NewSession(_ context.Context, req acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	a.mu.Lock()
	a.news = append(a.news, req)
	onNew := a.onNew
	a.mu.Unlock()
	if onNew != nil {
		return onNew(req)
	}
	return a.defaultNewSession(req)
}

// defaultNewSession mints a session id unique to this child, so a test can tell
// which child served a request.
func (a *stubAgent) defaultNewSession(req acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	a.mu.Lock()
	a.nextSession++
	id := acp.SessionId(fmt.Sprintf("%s-session-%d", a.name, a.nextSession))
	a.mu.Unlock()
	return acp.NewSessionResponse{
		SessionId: id,
		Meta:      map[string]any{"chord": map[string]any{"sessionId": string(id) + "-dir"}},
	}, nil
}

func (a *stubAgent) Prompt(ctx context.Context, req acp.PromptRequest) (acp.PromptResponse, error) {
	a.mu.Lock()
	a.prompts = append(a.prompts, req)
	onPrompt := a.onPrompt
	a.mu.Unlock()
	if onPrompt != nil {
		return onPrompt(ctx, req)
	}
	update := acp.SessionNotification{SessionId: req.SessionId, Update: acp.UpdateAgentMessageText("hello")}
	if err := a.sendUpdate(ctx, update); err != nil {
		return acp.PromptResponse{}, err
	}
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

func (a *stubAgent) Cancel(_ context.Context, params acp.CancelNotification) error {
	a.mu.Lock()
	a.cancels = append(a.cancels, params)
	a.mu.Unlock()
	return nil
}

func (a *stubAgent) CloseSession(_ context.Context, req acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	a.mu.Lock()
	a.closes = append(a.closes, req)
	onClose := a.onClose
	a.mu.Unlock()
	if onClose != nil {
		return onClose(req)
	}
	return acp.CloseSessionResponse{}, nil
}

func (a *stubAgent) Authenticate(_ context.Context, _ acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, acp.NewMethodNotFound(acp.AgentMethodAuthenticate)
}

func (a *stubAgent) Logout(_ context.Context, _ acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, acp.NewMethodNotFound(acp.AgentMethodLogout)
}

func (a *stubAgent) ListSessions(_ context.Context, _ acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionList)
}

func (a *stubAgent) ResumeSession(_ context.Context, _ acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionResume)
}

func (a *stubAgent) SetSessionMode(_ context.Context, _ acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionSetMode)
}

func (a *stubAgent) SetSessionConfigOption(_ context.Context, _ acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	return acp.SetSessionConfigOptionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionSetConfigOption)
}

func (a *stubAgent) sendUpdate(ctx context.Context, update acp.SessionNotification) error {
	a.mu.Lock()
	conn := a.conn
	a.mu.Unlock()
	return conn.SessionUpdate(ctx, update)
}

func (a *stubAgent) initRequests() []acp.InitializeRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]acp.InitializeRequest(nil), a.init...)
}

func (a *stubAgent) newRequests() []acp.NewSessionRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]acp.NewSessionRequest(nil), a.news...)
}

func (a *stubAgent) promptRequests() []acp.PromptRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]acp.PromptRequest(nil), a.prompts...)
}

func (a *stubAgent) cancelRequests() []acp.CancelNotification {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]acp.CancelNotification(nil), a.cancels...)
}

func (a *stubAgent) closeRequests() []acp.CloseSessionRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]acp.CloseSessionRequest(nil), a.closes...)
}

func (a *stubAgent) promptCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.prompts)
}

func (a *stubAgent) newCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.news)
}

func (a *stubAgent) setOnNew(fn func(acp.NewSessionRequest) (acp.NewSessionResponse, error)) {
	a.mu.Lock()
	a.onNew = fn
	a.mu.Unlock()
}

func (a *stubAgent) setOnPrompt(fn func(context.Context, acp.PromptRequest) (acp.PromptResponse, error)) {
	a.mu.Lock()
	a.onPrompt = fn
	a.mu.Unlock()
}

// recordingClient is the ACP client Zed stands in for: it records what the
// frontend forwards and answers the child's permission requests.
type recordingClient struct {
	mu      sync.Mutex
	updates []acp.SessionNotification
	perms   []acp.RequestPermissionRequest
}

var _ acp.Client = (*recordingClient)(nil)

func (c *recordingClient) SessionUpdate(_ context.Context, params acp.SessionNotification) error {
	c.mu.Lock()
	c.updates = append(c.updates, params)
	c.mu.Unlock()
	return nil
}

func (c *recordingClient) RequestPermission(_ context.Context, params acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	c.mu.Lock()
	c.perms = append(c.perms, params)
	c.mu.Unlock()
	return acp.RequestPermissionResponse{
		Outcome: acp.RequestPermissionOutcome{
			Selected: &acp.RequestPermissionOutcomeSelected{OptionId: "allow", Outcome: "selected"},
		},
	}, nil
}

func (c *recordingClient) ReadTextFile(_ context.Context, _ acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, acp.NewMethodNotFound(acp.ClientMethodFsReadTextFile)
}

func (c *recordingClient) WriteTextFile(_ context.Context, _ acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, acp.NewMethodNotFound(acp.ClientMethodFsWriteTextFile)
}

func (c *recordingClient) CreateTerminal(_ context.Context, _ acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, acp.NewMethodNotFound(acp.ClientMethodTerminalCreate)
}

func (c *recordingClient) KillTerminal(_ context.Context, _ acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, acp.NewMethodNotFound(acp.ClientMethodTerminalKill)
}

func (c *recordingClient) TerminalOutput(_ context.Context, _ acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, acp.NewMethodNotFound(acp.ClientMethodTerminalOutput)
}

func (c *recordingClient) ReleaseTerminal(_ context.Context, _ acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, acp.NewMethodNotFound(acp.ClientMethodTerminalRelease)
}

func (c *recordingClient) WaitForTerminalExit(_ context.Context, _ acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, acp.NewMethodNotFound(acp.ClientMethodTerminalWaitForExit)
}

func (c *recordingClient) updatesFor(session acp.SessionId) []acp.SessionNotification {
	c.mu.Lock()
	defer c.mu.Unlock()
	var matched []acp.SessionNotification
	for _, update := range c.updates {
		if update.SessionId == session {
			matched = append(matched, update)
		}
	}
	return matched
}

func (c *recordingClient) permissionRequests() []acp.RequestPermissionRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]acp.RequestPermissionRequest(nil), c.perms...)
}
