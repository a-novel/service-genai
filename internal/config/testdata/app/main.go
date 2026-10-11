package main

import (
	"fmt"

	"github.com/a-novel/service-genai/internal/config"
)

func main() {
	fmt.Println(config.AppPresetDefault.Generations.CheckInterval)
}
