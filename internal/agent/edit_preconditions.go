package agent

import "fmt"

func wrapTrackedWriteError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("file conflict: %w", err)
}
