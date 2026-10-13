//go:build !darwin && !linux && !windows

package desktop

import "context"

func (s *System) Capabilities(context.Context) (Capabilities, error) {
	return Capabilities{Reason: ErrUnsupported.Error()}, nil
}
func (s *System) queryAssociations(context.Context, string) (Associations, error) {
	return Associations{}, ErrUnsupported
}
func (s *System) OpenDefault(context.Context, string) error       { return ErrUnsupported }
func (s *System) OpenWith(context.Context, string, string) error  { return ErrUnsupported }
func (s *System) ChooseApplication(context.Context, string) error { return ErrUnsupported }
