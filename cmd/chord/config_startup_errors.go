package main

import (
	"errors"
	"fmt"
	"os"
)

const initialSetupRequiredMessage = "no config.yaml found; run `chord` once in an interactive terminal to complete initial setup"

func initialSetupRequiredError() error {
	return errors.New(initialSetupRequiredMessage)
}

func wrapConfigLoadError(action string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return initialSetupRequiredError()
	}
	if action == "" {
		return err
	}
	return fmt.Errorf("%s: %w", action, err)
}
