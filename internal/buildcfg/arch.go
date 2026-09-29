package buildcfg

import (
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

// HostArch returns the Debian architecture name for the host machine.
//
// gl-ng targets Debian, whose architecture vocabulary (amd64, arm64, …) does
// not match Go's GOARCH (amd64, arm64 happen to coincide; 386 vs i386,
// arm/armhf, ppc64le, etc. do not). dpkg --print-architecture is the
// authoritative answer when available; otherwise we map runtime.GOARCH.
//
// The result is the *default* target architecture for builds. Callers that
// accept --arch on the command line should pass HostArch() as the default
// rather than hard-coding "amd64": cross-arch builds in this project rely on
// binfmt_misc + qemu-user-static, but the *default* must always be native to
// avoid silently doing emulated work on non-x86_64 hosts.
func HostArch() string {
	hostArchOnce.Do(func() {
		hostArch = detectHostArch()
	})
	return hostArch
}

var (
	hostArchOnce sync.Once
	hostArch     string
)

func detectHostArch() string {
	if out, err := exec.Command("dpkg", "--print-architecture").Output(); err == nil {
		if a := strings.TrimSpace(string(out)); a != "" {
			return a
		}
	}
	return goArchToDebian(runtime.GOARCH)
}

func goArchToDebian(goarch string) string {
	switch goarch {
	case "amd64":
		return "amd64"
	case "arm64":
		return "arm64"
	case "386":
		return "i386"
	case "arm":
		return "armhf"
	case "ppc64le":
		return "ppc64el"
	case "s390x":
		return "s390x"
	case "riscv64":
		return "riscv64"
	default:
		return goarch
	}
}
