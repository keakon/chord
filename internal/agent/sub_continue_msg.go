package agent

type continueMsg struct {
	pauseSpecDone       bool
	pauseCapture        *subPauseCaptureRequest
	stopPaused          chan error
	cancelCurrentTurn   bool
	drainContextAppends bool
	restartStoppedTurn  bool
}
