package main

import (
	"context"
	embed
	"os"
)

//go:embed build/appicon.png
var appIcon []byte

func main() { _ = context.Background(); _ = embed.FS(nil); _ = os.Args; _ = appIcon }