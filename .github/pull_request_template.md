## Summary

- What does this change do?
- Why is it needed?

## Checklist

- [ ] I ran `go test -coverprofile=coverage.out ./...`
- [ ] I reviewed coverage with `go tool cover -func=coverage.out` and total coverage is at least 65.0%
- [ ] I ran `go vet ./...`
- [ ] I ran `staticcheck -checks 'all,-ST*' ./...`
- [ ] I updated docs for user-facing behavior, CLI/config/workflow changes, or development command changes
- [ ] I added/updated unit or regression tests when changing behavior

## Notes for reviewers

- Any context on risk, compatibility, or follow-ups
