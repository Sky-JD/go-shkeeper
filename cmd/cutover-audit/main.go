package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/Sky-JD/go-shkeeper/internal/cutoveraudit"
)

func main() {
	cfg, err := cutoveraudit.LoadConfigFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cutover-audit config failed: %v\n", err)
		os.Exit(1)
	}
	audit, err := cutoveraudit.Run(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cutover-audit failed: %v\n", err)
		os.Exit(1)
	}
	if cfg.OutputFile != "" {
		if err := cutoveraudit.WriteAudit(cfg.OutputFile, audit); err != nil {
			fmt.Fprintf(os.Stderr, "cutover-audit write failed: %v\n", err)
			os.Exit(1)
		}
	}
	data, _ := json.MarshalIndent(audit, "", "  ")
	fmt.Println(string(data))
	if audit.Status != "pass" {
		os.Exit(1)
	}
}
