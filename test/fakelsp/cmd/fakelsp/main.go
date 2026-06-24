// Command fakelsp is a standalone build of the deterministic fake language
// server used by bezalel's e2e tests. It is intentionally not part of the
// production binary.
package main

import "github.com/aleksclark/bezalel/test/fakelsp"

func main() {
	fakelsp.Run()
}
