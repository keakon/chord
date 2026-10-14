// Package requesttrim derives the tool-result surface of one model request.
// It owns reduction rules, read-evidence analysis and reusable parse memos;
// callers retain the durable transcript and decide when to apply the results.
// Classification never mutates messages. Render may archive a complete output
// under Context.ArchiveDir before returning its recoverable replacement.
package requesttrim
