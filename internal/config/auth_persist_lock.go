package config

type authFileLock struct {
	lock *configMutationLock
}

func lockAuthFile(path string) (*authFileLock, error) {
	lock, err := LockConfigMutation(path)
	if err != nil {
		return nil, err
	}
	return &authFileLock{lock: lock}, nil
}

func (l *authFileLock) Close() error {
	if l == nil || l.lock == nil {
		return nil
	}
	err := l.lock.Close()
	l.lock = nil
	return err
}
