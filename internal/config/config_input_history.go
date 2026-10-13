package config

import "github.com/keakon/chord/internal/inputhistory"

// InputHistoryConfig is user-scoped; pointers preserve explicit false and zero.
type InputHistoryConfig struct {
	Enabled    *bool  `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	MaxEntries *int   `json:"max_entries,omitempty" yaml:"max_entries,omitempty"`
	MaxBytes   *int64 `json:"max_bytes,omitempty" yaml:"max_bytes,omitempty"`
}

func (c InputHistoryConfig) Effective() (inputhistory.Options, error) {
	o := inputhistory.DefaultOptions()
	if c.Enabled != nil {
		o.Enabled = *c.Enabled
	}
	if c.MaxEntries != nil {
		o.MaxEntries = *c.MaxEntries
	}
	if c.MaxBytes != nil {
		o.MaxBytes = *c.MaxBytes
	}
	return o, o.Validate()
}
