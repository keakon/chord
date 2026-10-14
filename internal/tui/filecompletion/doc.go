// Package filecompletion resolves file references and ranks path completion
// candidates. It owns fuzzy narrowing state; callers own the file index, input
// editing, refresh generations and asynchronous scanning.
package filecompletion
