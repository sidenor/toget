// Command toget-official is the future directory, account host, relay, and
// update service. It is a composition of the same internal packages as the
// self-hosted node, not a separate implementation.
package main

import (
	"fmt"

	"github.com/sidenor/toget/internal/version"
)

func main() {
	info := version.Current()
	fmt.Printf("toget-official %s (%s/%s, protocol %d)\n", info.App, info.OS, info.Arch, info.Protocol)
	fmt.Println()
	fmt.Println("The official directory, account host, relay, and update service are not implemented yet.")
	fmt.Println("For now, run `toget serve` on your own machine.")
}
