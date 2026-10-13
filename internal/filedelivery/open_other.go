//go:build !unix

package filedelivery

import "os"

func openRegularCandidate(path string) (*os.File, error)             { return os.Open(path) }
func openRootCandidate(root *os.Root, path string) (*os.File, error) { return root.Open(path) }
