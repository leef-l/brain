package main

import (
	"fmt"
	"github.com/leef-l/brain/cmd/brain/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Println("load error:", err)
		return
	}
	if cfg == nil {
		fmt.Println("cfg is nil")
		return
	}
	fmt.Println("active_provider:", cfg.ActiveProvider)
	fmt.Println("providers count:", len(cfg.Providers))
	if p, ok := cfg.Providers["deepseek"]; ok {
		fmt.Println("deepseek base_url:", p.BaseURL)
		fmt.Println("deepseek api_key len:", len(p.APIKey))
		fmt.Println("deepseek model:", p.Model)
		fmt.Println("deepseek protocol:", p.Protocol)
	} else {
		fmt.Println("deepseek provider not found")
	}
}
