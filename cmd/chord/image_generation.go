package main

import (
	"fmt"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/tools"
)

func configureImageGeneration(ac *AppContext) error {
	c := ac.Cfg.ImageGeneration
	if !c.Enabled {
		return nil
	}
	if err := c.Validate(ac.Cfg); err != nil {
		return err
	}
	var backends []tools.ImageGenerationBackend
	for _, ref := range ac.Cfg.ModelPools[c.ModelPool] {
		name, model, _, provider, mc, err := config.ResolveConfiguredModelRef(ac.Cfg.Providers, ref)
		if err != nil {
			return err
		}
		target, err := mc.ImageGeneration.Target(name, model, provider)
		if err != nil {
			return err
		}
		keys := resolveProviderAPIKeys(name, provider, ac.Auth, ac.CredDecls)
		backend, err := agent.NewImageGenerationBackend(ac.MainAgent, target, c.Timeout(), provider, keys, ac.Cfg.Proxy)
		if err != nil {
			return fmt.Errorf("image generation %s: %w", ref, err)
		}
		backends = append(backends, backend)
	}
	backend := agent.NewImageGenerationPool(ac.MainAgent, backends)
	ac.Registry.Register(&tools.GenerateImageTool{Backend: backend, BaseDir: ac.WorkDir})
	return nil
}
