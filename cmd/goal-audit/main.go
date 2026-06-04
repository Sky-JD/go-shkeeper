package main

import (
	"fmt"
	"os"

	"github.com/Sky-JD/go-shkeeper/internal/goalaudit"
)

func main() {
	cfg := goalaudit.LoadConfigFromEnv()
	report, err := goalaudit.Run(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "goal-audit failed:", err)
		os.Exit(1)
	}
	if err := goalaudit.WriteReport(cfg.OutputFile, report); err != nil {
		fmt.Fprintln(os.Stderr, "goal-audit write failed:", err)
		os.Exit(1)
	}
	if report.Status != goalaudit.StatusPass {
		os.Exit(1)
	}
}
