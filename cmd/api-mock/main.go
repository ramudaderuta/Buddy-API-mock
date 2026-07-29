package main

import (
	"fmt"
	"os"

	"github.com/ramudaderuta/Buddy-API-mock/internal/apimock"
)

func main() {
	if err := apimock.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
