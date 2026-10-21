// Package game verifies an Echo VR game tree and builds per-instance views of
// it, so many servers can share one read-only copy.
package game

import (
	"crypto/sha256"
	"debug/pe"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Build describes a game build nevr-runtime supports.
type Build struct {
	Version       string
	ExeSHA256     string
	TimeDateStamp uint32
}

// Supported is the only build nevr-runtime 5.x accepts (game_image_guard.cpp).
var Supported = Build{
	Version:       "34.4.631547.1",
	ExeSHA256:     "b6d08277e5846900c81004b64b298df6acba834b69700a640b758bda94a52043",
	TimeDateStamp: 0x6452dff6,
}

// Paths inside a game tree.
const (
	BinDir       = "bin/win10"
	ExeName      = "echovr.exe"
	NetconfigRel = "sourcedb/rad15/json/r14/config/netconfig_dedicatedserver.json"
)

// Info is the result of verifying a game tree.
type Info struct {
	Dir       string
	Build     Build
	ExeSHA256 string
	// LegacyFiles are files that conflict with nevr-runtime when placed next to
	// echovr.exe. The symlink farm leaves them out; a plain copy must not have them.
	LegacyFiles []string
}

// ErrUnsupportedBuild means echovr.exe is not the build nevr-runtime expects.
var ErrUnsupportedBuild = errors.New("unsupported echovr.exe build")

// Verify checks that dir is a complete game tree of the supported build.
func Verify(dir string) (Info, error) {
	info := Info{Dir: dir}
	for _, rel := range []string{ExeName, "pnsrad.dll", "pnsradgameserver.dll"} {
		if _, err := os.Stat(filepath.Join(dir, BinDir, rel)); err != nil {
			return info, fmt.Errorf("%s is not an Echo VR game tree: %w", dir, err)
		}
	}
	for _, rel := range []string{"content", "sourcedb", NetconfigRel} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			return info, fmt.Errorf("game tree %s is incomplete: %w", dir, err)
		}
	}
	exe := filepath.Join(dir, BinDir, ExeName)
	sum, err := fileSHA256(exe)
	if err != nil {
		return info, err
	}
	info.ExeSHA256 = sum
	if sum != Supported.ExeSHA256 {
		stamp, _ := peTimeDateStamp(exe)
		return info, fmt.Errorf("%w: sha256 %s (PE timestamp %#x), need %s (%s)", ErrUnsupportedBuild, sum, stamp, Supported.Version, Supported.ExeSHA256)
	}
	info.Build = Supported
	for _, name := range legacyFiles {
		if _, err := os.Lstat(filepath.Join(dir, BinDir, name)); err == nil {
			info.LegacyFiles = append(info.LegacyFiles, filepath.Join(BinDir, name))
		}
	}
	return info, nil
}

// legacyFiles are loaders from the EchoRelay/dbgcore era. A dbgcore.dll next to
// the runtime makes it refuse to start.
var legacyFiles = []string{"dbgcore.dll"}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func peTimeDateStamp(path string) (uint32, error) {
	f, err := pe.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return f.FileHeader.TimeDateStamp, nil
}
