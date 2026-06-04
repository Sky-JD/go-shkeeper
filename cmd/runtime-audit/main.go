package main

import (
	"fmt"
	"os"

	"github.com/Sky-JD/go-shkeeper/internal/runtimeaudit"
)

func main() {
	result, err := runtimeaudit.Run(runtimeaudit.CommandsFromEnv(), runtimeaudit.RequiredFilesFromEnv())
	data, marshalErr := runtimeaudit.Marshal(result)
	if marshalErr != nil {
		fmt.Fprintf(os.Stderr, "runtime audit marshal failed: %v\n", marshalErr)
		os.Exit(1)
	}
	fmt.Println(string(data))
	if err != nil {
		os.Exit(1)
	}
}
