package tools

import "context"

// ImageAccessGuard rechecks the approved call's actual resources immediately
// before reading or sending them, using its captured filesystem binding.
type ImageAccessGuard func(tool, path string) error

type imageAccessGuardKey struct{}

func WithImageAccessGuard(ctx context.Context, guard ImageAccessGuard) context.Context {
	return context.WithValue(ctx, imageAccessGuardKey{}, guard)
}

func ImageAccessGuardFromContext(ctx context.Context) (ImageAccessGuard, bool) {
	guard, ok := ctx.Value(imageAccessGuardKey{}).(ImageAccessGuard)
	return guard, ok && guard != nil
}
