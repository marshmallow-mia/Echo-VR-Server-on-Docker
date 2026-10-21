// evr-docker installs and manages Echo VR dedicated servers on a Docker host.
package main

import (
	"os"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/internal/cli"
)

var version = "dev"

func main() {
	cli.Version = version
	os.Exit(cli.Execute())
}
