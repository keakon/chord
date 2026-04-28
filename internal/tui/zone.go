package tui

// ZoneBounds holds the screen coordinates of a named UI zone.
type ZoneBounds struct {
	StartX, StartY, EndX, EndY int
}

// IsZero reports whether this ZoneBounds has never been set.
func (z ZoneBounds) IsZero() bool {
	return z.StartX == 0 && z.StartY == 0 && z.EndX == 0 && z.EndY == 0
}

// ZoneManager records the screen bounds of named UI zones.
// It replaces github.com/lrstanley/bubblezone: instead of embedding escape
// sequences in rendered text and scanning them back out, we compute bounds
// directly from the known layout and store them here for mouse hit-testing.
type ZoneManager struct {
	zones map[string]ZoneBounds
}

func newZoneManager() *ZoneManager {
	return &ZoneManager{zones: make(map[string]ZoneBounds)}
}

func (z *ZoneManager) Set(id string, b ZoneBounds) {
	z.zones[id] = b
}

func (z *ZoneManager) Get(id string) ZoneBounds {
	if z == nil {
		return ZoneBounds{}
	}
	return z.zones[id]
}
