// evr-docker installs and manages Echo VR dedicated servers on a Docker host.
package main

import (
	"os"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/internal/cli"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/internal/tui"
)

var version = "dev"

func main() {
	cli.Version = version
	cli.TUI = tui.Run
	os.Exit(cli.Execute())
}
