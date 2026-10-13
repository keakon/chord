package inputhistory

import (
	"os"

	"github.com/keakon/chord/internal/atomicfile"
)

// fileHooks exercise only the three failure boundaries used by the writer.
// They are immutable after construction and are never exposed outside this package.
type fileHooks struct {
	write   func(*os.File, []byte) (int, error)
	sync    func(*os.File) error
	replace func(string, []byte) error
}

func (s *Store) write(f *os.File, b []byte) (int, error) {
	if s.hooks.write != nil {
		return s.hooks.write(f, b)
	}
	return f.Write(b)
}
func (s *Store) syncFile(f *os.File) error {
	if s.hooks.sync != nil {
		return s.hooks.sync(f)
	}
	return f.Sync()
}
func (s *Store) replace(data []byte) error {
	if s.hooks.replace != nil {
		return s.hooks.replace(s.path, data)
	}
	return atomicfile.Replace(s.path, data, 0600)
}
