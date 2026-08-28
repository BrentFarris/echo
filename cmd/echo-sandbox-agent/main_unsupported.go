//go:build !linux

package main

import "fmt"

func main() { fmt.Println("echo-sandbox-agent is available only inside Linux sandbox images") }
