## Summary

- What does this change do?
- Why is it needed?

## Checklist

- [ ] I ran `make ci`, or the relevant CI-equivalent targets for this change
- [ ] I reviewed coverage with `make test-cover` or `scripts/check_ci_local.sh`, and total coverage is at least 70.0%
- [ ] I ran `go vet ./...`
- [ ] I ran `staticcheck -checks 'all,-ST1000' ./...`
- [ ] I ran dependency/docs checks when changing `go.mod`, CI scripts, or docs examples
- [ ] I updated docs for user-facing behavior, CLI/config/workflow changes, or development command changes
- [ ] I added/updated unit or regression tests when changing behavior

## Notes for reviewers

- Any context on risk, compatibility, or follow-ups
