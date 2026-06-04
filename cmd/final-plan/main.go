package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Sky-JD/go-shkeeper/internal/finalplan"
)

func main() {
	ctx := context.Background()
	cfg := finalplan.LoadConfigFromEnv()
	plan, err := finalplan.NewBuilder(cfg).Build(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "final-plan failed:", err)
		os.Exit(1)
	}
	if err := finalplan.Write(cfg.OutputFile, plan); err != nil {
		fmt.Fprintln(os.Stderr, "write final plan failed:", err)
		os.Exit(1)
	}
	if err := finalplan.WriteReadiness(cfg.ReadinessFile, finalplan.Readiness(plan)); err != nil {
		fmt.Fprintln(os.Stderr, "write final plan readiness failed:", err)
		os.Exit(1)
	}
}
