package agent

type continueMsg struct {
	cancelCurrentTurn   bool
	drainContextAppends bool
	restartStoppedTurn  bool
}
