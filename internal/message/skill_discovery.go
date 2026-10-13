package message

const (
	KindSkillDiscovery        = "skill_discovery"
	SkillDiscoveryModeRead    = "read"
	SkillDiscoveryModeRestore = "restore"
)

type SkillActivationRef struct {
	Identity string `json:"identity"`
	Revision string `json:"revision"`
	Name     string `json:"name"`
}
type SkillActivationFact struct {
	TaskID       string               `json:"task_id"`
	CheckoutRoot string               `json:"checkout_root"`
	SourceCallID string               `json:"source_call_id"`
	ContextEpoch uint64               `json:"context_epoch"`
	Skills       []SkillActivationRef `json:"skills,omitempty"`
}
type SkillDiscoveryRecord struct {
	ID           string               `json:"id"`
	TaskID       string               `json:"task_id"`
	CheckoutRoot string               `json:"checkout_root"`
	ContextEpoch uint64               `json:"context_epoch"`
	Mode         string               `json:"mode"`
	Skills       []SkillActivationRef `json:"skills,omitempty"`
	Omitted      int                  `json:"omitted,omitempty"`
}
type SkillActivationCarry struct {
	TaskID       string                `json:"task_id"`
	CheckoutRoot string                `json:"checkout_root"`
	ContextEpoch uint64                `json:"context_epoch"`
	Facts        []SkillActivationFact `json:"facts,omitempty"`
}

func (f *SkillActivationFact) Clone() *SkillActivationFact {
	if f == nil {
		return nil
	}
	c := *f
	c.Skills = append([]SkillActivationRef(nil), f.Skills...)
	return &c
}
func (f *SkillDiscoveryRecord) Clone() *SkillDiscoveryRecord {
	if f == nil {
		return nil
	}
	c := *f
	c.Skills = append([]SkillActivationRef(nil), f.Skills...)
	return &c
}
func (f *SkillActivationCarry) Clone() *SkillActivationCarry {
	if f == nil {
		return nil
	}
	c := *f
	c.Facts = make([]SkillActivationFact, len(f.Facts))
	for i := range f.Facts {
		c.Facts[i] = *f.Facts[i].Clone()
	}
	return &c
}
