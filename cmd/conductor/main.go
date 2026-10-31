// Command conductor runs the shared agent terminal server (`serve`) or hosts a
// local terminal session from a developer machine (`host`). It is the free
// build: it registers no provider (package ext), so every extension seam runs
// its free default. A main in another module that registers providers calls
// app.Main the same way.
package main

import "github.com/rocksolidlabs/conductor/app"

func main() { app.Main() }
