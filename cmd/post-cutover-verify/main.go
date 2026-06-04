package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/Sky-JD/go-shkeeper/internal/postcutover"
)

func main() {
	cfg := postcutover.LoadConfigFromEnv()
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()
	result, err := postcutover.Verify(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "post-cutover verify failed: %v\n", err)
		os.Exit(1)
	}
	if cfg.OutputFile != "" {
		if err := postcutover.WriteResult(cfg.OutputFile, result); err != nil {
			fmt.Fprintf(os.Stderr, "post-cutover write failed: %v\n", err)
			os.Exit(1)
		}
	}
	data, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(data))
	if result.Status != "pass" {
		os.Exit(1)
	}
}
