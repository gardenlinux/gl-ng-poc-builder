package container

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gl-ng/internal/ipc"
	"gl-ng/internal/log"
)

func testCtx() context.Context {
	return log.WithTarget(context.Background(), log.Discard)
}

// =============================================================================
// Layer 1: BaseExecEnv — direct process spawning, no namespaces
// =============================================================================

func TestBaseExecTrue(t *testing.T) {
	base := NewBaseExecEnv()
	defer base.Close()

	pid, err := base.Exec(&ExecRequest{Argv: []string{"/bin/true"}})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	code, err := base.Wait(pid)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if code != 0 {
		t.Fatalf("expected 0, got %d", code)
	}
}

func TestBaseExecFalse(t *testing.T) {
	base := NewBaseExecEnv()
	defer base.Close()

	pid, err := base.Exec(&ExecRequest{Argv: []string{"/bin/false"}})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	code, err := base.Wait(pid)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if code != 1 {
		t.Fatalf("expected 1, got %d", code)
	}
}

func TestBaseExecStdoutCapture(t *testing.T) {
	base := NewBaseExecEnv()
	defer base.Close()

	r, w, _ := os.Pipe()
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()

	pid, err := base.Exec(&ExecRequest{
		Argv: []string{"/bin/sh", "-c", "echo hello"},
		FDs:  []*os.File{devNull, w, devNull},
	})
	w.Close()
	if err != nil {
		r.Close()
		t.Fatalf("exec: %v", err)
	}

	out, _ := io.ReadAll(r)
	r.Close()
	code, _ := base.Wait(pid)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if strings.TrimSpace(string(out)) != "hello" {
		t.Fatalf("expected 'hello', got %q", string(out))
	}
}

func TestBaseExecStdinPipe(t *testing.T) {
	base := NewBaseExecEnv()
	defer base.Close()

	stdinR, stdinW, _ := os.Pipe()
	stdoutR, stdoutW, _ := os.Pipe()
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()

	pid, err := base.Exec(&ExecRequest{
		Argv: []string{"/bin/cat"},
		FDs:  []*os.File{stdinR, stdoutW, devNull},
	})
	stdinR.Close()
	stdoutW.Close()
	if err != nil {
		t.Fatalf("exec: %v", err)
	}

	stdinW.Write([]byte("piped data\n"))
	stdinW.Close()

	out, _ := io.ReadAll(stdoutR)
	stdoutR.Close()
	code, _ := base.Wait(pid)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if string(out) != "piped data\n" {
		t.Fatalf("expected 'piped data\\n', got %q", string(out))
	}
}

func TestBaseExecEnvVars(t *testing.T) {
	base := NewBaseExecEnv()
	defer base.Close()

	r, w, _ := os.Pipe()
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()

	pid, err := base.Exec(&ExecRequest{
		Argv: []string{"/bin/sh", "-c", "echo $FOO"},
		Env:  []string{"FOO=bar42"},
		FDs:  []*os.File{devNull, w, devNull},
	})
	w.Close()
	if err != nil {
		r.Close()
		t.Fatalf("exec: %v", err)
	}

	out, _ := io.ReadAll(r)
	r.Close()
	code, _ := base.Wait(pid)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if strings.TrimSpace(string(out)) != "bar42" {
		t.Fatalf("expected 'bar42', got %q", string(out))
	}
}

func TestBaseExecCwd(t *testing.T) {
	base := NewBaseExecEnv()
	defer base.Close()

	r, w, _ := os.Pipe()
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()

	pid, err := base.Exec(&ExecRequest{
		Argv: []string{"/bin/sh", "-c", "pwd"},
		Cwd:  "/tmp",
		FDs:  []*os.File{devNull, w, devNull},
	})
	w.Close()
	if err != nil {
		r.Close()
		t.Fatalf("exec: %v", err)
	}

	out, _ := io.ReadAll(r)
	r.Close()
	code, _ := base.Wait(pid)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if strings.TrimSpace(string(out)) != "/tmp" {
		t.Fatalf("expected '/tmp', got %q", string(out))
	}
}

func TestBaseExecInvalidBinary(t *testing.T) {
	base := NewBaseExecEnv()
	defer base.Close()

	_, err := base.Exec(&ExecRequest{Argv: []string{"/nonexistent/binary"}})
	if err == nil {
		t.Fatal("expected error for nonexistent binary")
	}
}

// =============================================================================
// IDMapping computation tests
// =============================================================================

func TestIDMappingSingleRange(t *testing.T) {
	subRanges := []IDRange{{Start: 100000, Count: 65536}}
	parentMap := []IDMapping{{Inner: 0, Outer: 0, Count: 4294967295}}

	mappings, err := ComputeIDMappings(subRanges, parentMap, 65536)
	if err != nil {
		t.Fatal(err)
	}
	if len(mappings) != 1 {
		t.Fatalf("expected 1 mapping, got %d", len(mappings))
	}
	if mappings[0].Inner != 0 || mappings[0].Outer != 100000 || mappings[0].Count != 65536 {
		t.Fatalf("unexpected mapping: %+v", mappings[0])
	}
}

func TestIDMappingInsufficientIDs(t *testing.T) {
	subRanges := []IDRange{{Start: 100000, Count: 100}}
	parentMap := []IDMapping{{Inner: 0, Outer: 0, Count: 4294967295}}

	_, err := ComputeIDMappings(subRanges, parentMap, 65536)
	if err == nil {
		t.Fatal("expected error for insufficient IDs")
	}
}

func TestIDMappingMultipleRanges(t *testing.T) {
	subRanges := []IDRange{
		{Start: 100000, Count: 30000},
		{Start: 200000, Count: 40000},
	}
	parentMap := []IDMapping{{Inner: 0, Outer: 0, Count: 4294967295}}

	mappings, err := ComputeIDMappings(subRanges, parentMap, 65536)
	if err != nil {
		t.Fatal(err)
	}
	if len(mappings) != 2 {
		t.Fatalf("expected 2 mappings, got %d", len(mappings))
	}

	var total uint32
	for _, m := range mappings {
		total += m.Count
	}
	if total != 65536 {
		t.Fatalf("expected total 65536, got %d", total)
	}
}

// =============================================================================
// Layer 2: UserNS — user namespace with UID mapping, process runs inside
// =============================================================================

func requireStub(t *testing.T) string {
	t.Helper()
	stubPath := os.Getenv("GL_EXEC_ENV_STUB")
	if stubPath == "" {
		t.Skip("GL_EXEC_ENV_STUB not set; run `make build` and `make test` (the Makefile sets it for you)")
	}
	if _, err := os.Stat(stubPath); err != nil {
		t.Skipf("GL_EXEC_ENV_STUB=%q does not exist; run `make build` first", stubPath)
	}
	return stubPath
}

func TestUserNSExecTrue(t *testing.T) {
	stubPath := requireStub(t)
	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	pid, err := userNS.Exec(&ExecRequest{
		Argv: []string{"/bin/true"},
		Env:  []string{"PATH=/bin"},
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	code, err := userNS.Wait(pid)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if code != 0 {
		t.Fatalf("expected 0, got %d", code)
	}
}

func TestUserNSExecStdoutCapture(t *testing.T) {
	stubPath := requireStub(t)
	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	r, w, _ := os.Pipe()
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()

	pid, err := userNS.Exec(&ExecRequest{
		Argv: []string{"/bin/sh", "-c", "echo userns_hello"},
		Env:  []string{"PATH=/bin:/usr/bin"},
		FDs:  []*os.File{devNull, w, devNull},
	})
	w.Close()
	if err != nil {
		r.Close()
		t.Fatalf("exec: %v", err)
	}

	out, _ := io.ReadAll(r)
	r.Close()
	code, _ := userNS.Wait(pid)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if strings.TrimSpace(string(out)) != "userns_hello" {
		t.Fatalf("expected 'userns_hello', got %q", string(out))
	}
}

func TestUserNSUIDIsRoot(t *testing.T) {
	stubPath := requireStub(t)
	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	r, w, _ := os.Pipe()
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()

	pid, err := userNS.Exec(&ExecRequest{
		Argv: []string{"/bin/sh", "-c", "cat /proc/self/status"},
		Env:  []string{"PATH=/bin:/usr/bin"},
		FDs:  []*os.File{devNull, w, devNull},
	})
	w.Close()
	if err != nil {
		r.Close()
		t.Fatalf("exec: %v", err)
	}

	out, _ := io.ReadAll(r)
	r.Close()
	code, _ := userNS.Wait(pid)
	if code != 0 {
		t.Fatalf("exit %d, output: %s", code, string(out))
	}

	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Uid:") {
			fields := strings.Fields(line)
			if len(fields) < 2 || fields[1] != "0" {
				t.Fatalf("expected uid 0, got: %s", line)
			}
			return
		}
	}
	t.Fatalf("Uid line not found in output:\n%s", string(out))
}

func TestUserNSCredentialSwitchUID1000(t *testing.T) {
	stubPath := requireStub(t)
	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	r, w, _ := os.Pipe()
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()

	pid, err := userNS.Exec(&ExecRequest{
		Argv:        []string{"/bin/sh", "-c", "cat /proc/self/status"},
		Env:         []string{"PATH=/bin:/usr/bin"},
		Credentials: &Credentials{UID: 1000, GID: 1000},
		FDs:         []*os.File{devNull, w, devNull},
	})
	w.Close()
	if err != nil {
		r.Close()
		t.Fatalf("exec: %v", err)
	}

	out, _ := io.ReadAll(r)
	r.Close()
	code, _ := userNS.Wait(pid)
	if code != 0 {
		t.Fatalf("exit %d, output: %s", code, string(out))
	}

	var foundUID, foundGID bool
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Uid:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[1] == "1000" {
				foundUID = true
			} else {
				t.Fatalf("expected uid 1000, got: %s", line)
			}
		}
		if strings.HasPrefix(line, "Gid:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[1] == "1000" {
				foundGID = true
			} else {
				t.Fatalf("expected gid 1000, got: %s", line)
			}
		}
	}
	if !foundUID {
		t.Fatalf("Uid line not found")
	}
	if !foundGID {
		t.Fatalf("Gid line not found")
	}
}

func TestUserNSMultipleExecs(t *testing.T) {
	stubPath := requireStub(t)
	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	for i := 0; i < 5; i++ {
		pid, err := userNS.Exec(&ExecRequest{
			Argv: []string{"/bin/true"},
			Env:  []string{"PATH=/bin"},
		})
		if err != nil {
			t.Fatalf("exec %d: %v", i, err)
		}
		code, err := userNS.Wait(pid)
		if err != nil {
			t.Fatalf("wait %d: %v", i, err)
		}
		if code != 0 {
			t.Fatalf("exec %d: expected 0, got %d", i, code)
		}
	}
}

// =============================================================================
// Layer 3: MountNS — mount namespace on top of UserNS, can mount tmpfs
// =============================================================================

func TestMountNSExecTrue(t *testing.T) {
	stubPath := requireStub(t)
	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	mountNS, err := NewMountNS(MountNSConfig{Parent: userNS, StubPath: stubPath})
	if err != nil {
		t.Fatalf("create mountns: %v", err)
	}
	defer mountNS.Close()

	pid, err := mountNS.Exec(&ExecRequest{
		Argv: []string{"/bin/true"},
		Env:  []string{"PATH=/bin"},
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	code, err := mountNS.Wait(pid)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if code != 0 {
		t.Fatalf("expected 0, got %d", code)
	}
}

func TestMountNSMountTmpfs(t *testing.T) {
	stubPath := requireStub(t)
	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	mountNS, err := NewMountNS(MountNSConfig{Parent: userNS, StubPath: stubPath})
	if err != nil {
		t.Fatalf("create mountns: %v", err)
	}
	defer mountNS.Close()

	tmpDir := t.TempDir()
	if err := mountNS.Mount("tmpfs", tmpDir, "tmpfs", 0, "mode=0755"); err != nil {
		t.Fatalf("mount tmpfs: %v", err)
	}

	// Write a file to the tmpfs via a process in the mount ns
	r, w, _ := os.Pipe()
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()

	pid, err := mountNS.Exec(&ExecRequest{
		Argv: []string{"/bin/sh", "-c", "echo mounted > " + tmpDir + "/test && cat " + tmpDir + "/test"},
		Env:  []string{"PATH=/bin:/usr/bin"},
		FDs:  []*os.File{devNull, w, devNull},
	})
	w.Close()
	if err != nil {
		r.Close()
		t.Fatalf("exec: %v", err)
	}

	out, _ := io.ReadAll(r)
	r.Close()
	code, _ := mountNS.Wait(pid)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if strings.TrimSpace(string(out)) != "mounted" {
		t.Fatalf("expected 'mounted', got %q", string(out))
	}
}

func TestMountNSMkdir(t *testing.T) {
	stubPath := requireStub(t)
	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	mountNS, err := NewMountNS(MountNSConfig{Parent: userNS, StubPath: stubPath})
	if err != nil {
		t.Fatalf("create mountns: %v", err)
	}
	defer mountNS.Close()

	tmpDir := t.TempDir()
	if err := mountNS.Mount("tmpfs", tmpDir, "tmpfs", 0, "mode=0755"); err != nil {
		t.Fatalf("mount tmpfs: %v", err)
	}

	newDir := tmpDir + "/subdir"
	if err := mountNS.Mkdir(newDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Verify the dir exists by listing it from inside the ns
	r, w, _ := os.Pipe()
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()

	pid, err := mountNS.Exec(&ExecRequest{
		Argv: []string{"/bin/sh", "-c", "test -d " + newDir + " && echo ok"},
		Env:  []string{"PATH=/bin"},
		FDs:  []*os.File{devNull, w, devNull},
	})
	w.Close()
	if err != nil {
		r.Close()
		t.Fatalf("exec: %v", err)
	}

	out, _ := io.ReadAll(r)
	r.Close()
	code, _ := mountNS.Wait(pid)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("expected 'ok', got %q", string(out))
	}
}

func TestMountNSMkTempDir(t *testing.T) {
	stubPath := requireStub(t)
	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	mountNS, err := NewMountNS(MountNSConfig{Parent: userNS, StubPath: stubPath})
	if err != nil {
		t.Fatalf("create mountns: %v", err)
	}
	defer mountNS.Close()

	tmpDir := t.TempDir()
	if err := mountNS.Mount("tmpfs", tmpDir, "tmpfs", 0, "mode=0755"); err != nil {
		t.Fatalf("mount tmpfs: %v", err)
	}

	dir1, err := mountNS.MkTempDir(tmpDir, "work-")
	if err != nil {
		t.Fatalf("MkTempDir: %v", err)
	}
	dir2, err := mountNS.MkTempDir(tmpDir, "work-")
	if err != nil {
		t.Fatalf("MkTempDir second: %v", err)
	}

	if dir1 == dir2 {
		t.Fatalf("expected distinct paths, got %q twice", dir1)
	}
	if filepath.Dir(dir1) != tmpDir || filepath.Dir(dir2) != tmpDir {
		t.Fatalf("unexpected parent: %q, %q", dir1, dir2)
	}

	// Verify both directories exist inside the namespace.
	for _, d := range []string{dir1, dir2} {
		r, w, _ := os.Pipe()
		devNull, _ := os.Open("/dev/null")
		defer devNull.Close()
		pid, err := mountNS.Exec(&ExecRequest{
			Argv: []string{"/bin/sh", "-c", "test -d " + d + " && echo ok"},
			Env:  []string{"PATH=/bin"},
			FDs:  []*os.File{devNull, w, devNull},
		})
		w.Close()
		if err != nil {
			r.Close()
			t.Fatalf("exec for %q: %v", d, err)
		}
		out, _ := io.ReadAll(r)
		r.Close()
		if code, _ := mountNS.Wait(pid); code != 0 {
			t.Fatalf("%q: exit %d", d, code)
		}
		if strings.TrimSpace(string(out)) != "ok" {
			t.Fatalf("%q: expected 'ok', got %q", d, string(out))
		}
	}
}

// buildMinimalRootfs creates a rootfs with /bin/sh, /bin/cat, /bin/true and
// their library dependencies (libc + dynamic linker). The set of libraries to
// copy and their target paths are discovered with ldd, so this works on any
// host architecture (amd64, arm64, …) without hard-coding paths.
func buildMinimalRootfs(t *testing.T) string {
	t.Helper()
	rootfs := t.TempDir()

	for _, d := range []string{"bin", "proc", "sys", "dev", "tmp", "run", "usr/bin"} {
		os.MkdirAll(rootfs+"/"+d, 0755)
	}

	binaries := map[string]string{
		"/bin/sh":   "bin/sh",
		"/bin/cat":  "bin/cat",
		"/bin/true": "bin/true",
	}
	libsToCopy := map[string]string{}
	for src := range binaries {
		for _, lib := range lddLibs(t, src) {
			// Strip leading slash to make rootfs-relative.
			libsToCopy[lib] = strings.TrimPrefix(lib, "/")
		}
	}
	// Make sure all parent directories under rootfs exist.
	for _, dst := range libsToCopy {
		os.MkdirAll(rootfs+"/"+filepath.Dir(dst), 0755)
	}

	for src, dst := range binaries {
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read %s: %v", src, err)
		}
		if err := os.WriteFile(rootfs+"/"+dst, data, 0755); err != nil {
			t.Fatalf("write %s: %v", dst, err)
		}
	}

	for src, dst := range libsToCopy {
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read %s: %v", src, err)
		}
		if err := os.WriteFile(rootfs+"/"+dst, data, 0755); err != nil {
			t.Fatalf("write %s: %v", dst, err)
		}
	}

	return rootfs
}

// lddLibs returns the absolute paths of every shared library that the dynamic
// linker would resolve for `bin`, including the linker itself. Pseudo-entries
// like linux-vdso (no on-disk path) are filtered out.
func lddLibs(t *testing.T, bin string) []string {
	t.Helper()
	out, err := exec.Command("ldd", bin).Output()
	if err != nil {
		t.Fatalf("ldd %s: %v", bin, err)
	}
	var libs []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Forms we care about:
		//   "libc.so.6 => /lib/aarch64-linux-gnu/libc.so.6 (0x...)"
		//   "/lib/ld-linux-aarch64.so.1 (0x...)"
		// Form we skip: "linux-vdso.so.1 (0x...)"
		if i := strings.Index(line, " => "); i >= 0 {
			rest := strings.TrimSpace(line[i+4:])
			if strings.HasPrefix(rest, "/") {
				if j := strings.Index(rest, " "); j > 0 {
					libs = append(libs, rest[:j])
				}
			}
		} else if strings.HasPrefix(line, "/") {
			if j := strings.Index(line, " "); j > 0 {
				libs = append(libs, line[:j])
			}
		}
	}
	return libs
}

func TestContainerExecTrue(t *testing.T) {
	stubPath := requireStub(t)
	rootfs := buildMinimalRootfs(t)

	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	cont, err := NewContainer(ContainerConfig{Ctx: testCtx(), Parent: userNS, StubPath: stubPath, Rootfs: rootfs})
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	defer cont.Close()

	pid, err := cont.Exec(&ExecRequest{
		Argv: []string{"/bin/true"},
		Env:  []string{"PATH=/bin"},
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	code, err := cont.Wait(pid)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if code != 0 {
		t.Fatalf("expected 0, got %d", code)
	}
}

func TestContainerExecStdoutCapture(t *testing.T) {
	stubPath := requireStub(t)
	rootfs := buildMinimalRootfs(t)

	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	cont, err := NewContainer(ContainerConfig{Ctx: testCtx(), Parent: userNS, StubPath: stubPath, Rootfs: rootfs})
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	defer cont.Close()

	r, w, _ := os.Pipe()
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()

	pid, err := cont.Exec(&ExecRequest{
		Argv: []string{"/bin/sh", "-c", "echo container_works"},
		Env:  []string{"PATH=/bin"},
		FDs:  []*os.File{devNull, w, devNull},
	})
	w.Close()
	if err != nil {
		r.Close()
		t.Fatalf("exec: %v", err)
	}

	out, _ := io.ReadAll(r)
	r.Close()
	code, _ := cont.Wait(pid)
	if code != 0 {
		t.Fatalf("exit %d, output: %s", code, string(out))
	}
	if strings.TrimSpace(string(out)) != "container_works" {
		t.Fatalf("expected 'container_works', got %q", string(out))
	}
}

func TestContainerProcMounted(t *testing.T) {
	stubPath := requireStub(t)
	rootfs := buildMinimalRootfs(t)

	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	cont, err := NewContainer(ContainerConfig{Ctx: testCtx(), Parent: userNS, StubPath: stubPath, Rootfs: rootfs})
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	defer cont.Close()

	r, w, _ := os.Pipe()
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()

	pid, err := cont.Exec(&ExecRequest{
		Argv: []string{"/bin/cat", "/proc/self/status"},
		Env:  []string{"PATH=/bin"},
		FDs:  []*os.File{devNull, w, devNull},
	})
	w.Close()
	if err != nil {
		r.Close()
		t.Fatalf("exec: %v", err)
	}

	out, _ := io.ReadAll(r)
	r.Close()
	code, _ := cont.Wait(pid)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}

	if !strings.Contains(string(out), "Uid:") {
		t.Fatalf("/proc/self/status should contain Uid, got:\n%s", string(out))
	}
}

func TestContainerPIDNamespace(t *testing.T) {
	stubPath := requireStub(t)
	rootfs := buildMinimalRootfs(t)

	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	cont, err := NewContainer(ContainerConfig{Ctx: testCtx(), Parent: userNS, StubPath: stubPath, Rootfs: rootfs})
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	defer cont.Close()

	r, w, _ := os.Pipe()
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()

	// Inside a PID namespace, the first child of the stub gets a low PID
	pid, err := cont.Exec(&ExecRequest{
		Argv: []string{"/bin/sh", "-c", "cat /proc/self/status"},
		Env:  []string{"PATH=/bin"},
		FDs:  []*os.File{devNull, w, devNull},
	})
	w.Close()
	if err != nil {
		r.Close()
		t.Fatalf("exec: %v", err)
	}

	out, _ := io.ReadAll(r)
	r.Close()
	code, _ := cont.Wait(pid)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}

	// In a PID namespace, the first process should have a low PID (the stub is PID 1,
	// its child /bin/sh is PID 2 or thereabouts)
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "NSpid:") {
			fields := strings.Fields(line)
			// NSpid shows PID in each namespace; the last field is the innermost
			innerPID := fields[len(fields)-1]
			// Should be a small number (typically 2-4)
			if innerPID == "0" || len(innerPID) > 3 {
				t.Fatalf("expected small inner PID, got NSpid line: %s", line)
			}
			return
		}
	}
	t.Log("NSpid not found — PID ns may not expose it in this kernel config, skipping assertion")
}

func TestContainerCredentials(t *testing.T) {
	stubPath := requireStub(t)
	rootfs := buildMinimalRootfs(t)

	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	cont, err := NewContainer(ContainerConfig{Ctx: testCtx(), Parent: userNS, StubPath: stubPath, Rootfs: rootfs})
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	defer cont.Close()

	r, w, _ := os.Pipe()
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()

	pid, err := cont.Exec(&ExecRequest{
		Argv:        []string{"/bin/cat", "/proc/self/status"},
		Env:         []string{"PATH=/bin"},
		Credentials: &Credentials{UID: 1000, GID: 1000},
		FDs:         []*os.File{devNull, w, devNull},
	})
	w.Close()
	if err != nil {
		r.Close()
		t.Fatalf("exec: %v", err)
	}

	out, _ := io.ReadAll(r)
	r.Close()
	code, _ := cont.Wait(pid)
	if code != 0 {
		t.Fatalf("exit %d, output: %s", code, string(out))
	}

	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Uid:") {
			fields := strings.Fields(line)
			if len(fields) < 2 || fields[1] != "1000" {
				t.Fatalf("expected uid 1000, got: %s", line)
			}
		}
		if strings.HasPrefix(line, "Gid:") {
			fields := strings.Fields(line)
			if len(fields) < 2 || fields[1] != "1000" {
				t.Fatalf("expected gid 1000, got: %s", line)
			}
		}
	}
}

func TestContainerIsolatedFilesystem(t *testing.T) {
	stubPath := requireStub(t)
	rootfs := buildMinimalRootfs(t)

	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	cont, err := NewContainer(ContainerConfig{Ctx: testCtx(), Parent: userNS, StubPath: stubPath, Rootfs: rootfs})
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	defer cont.Close()

	r, w, _ := os.Pipe()
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()

	// /etc should not exist in our minimal rootfs — verifies pivot_root worked
	pid, err := cont.Exec(&ExecRequest{
		Argv: []string{"/bin/sh", "-c", "test -d /etc && echo has_etc || echo no_etc"},
		Env:  []string{"PATH=/bin"},
		FDs:  []*os.File{devNull, w, devNull},
	})
	w.Close()
	if err != nil {
		r.Close()
		t.Fatalf("exec: %v", err)
	}

	out, _ := io.ReadAll(r)
	r.Close()
	code, _ := cont.Wait(pid)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if strings.TrimSpace(string(out)) != "no_etc" {
		t.Fatalf("expected filesystem isolation (no /etc), got %q", string(out))
	}
}

func TestContainerMultipleExecs(t *testing.T) {
	stubPath := requireStub(t)
	rootfs := buildMinimalRootfs(t)

	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	cont, err := NewContainer(ContainerConfig{Ctx: testCtx(), Parent: userNS, StubPath: stubPath, Rootfs: rootfs})
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	defer cont.Close()

	for i := 0; i < 5; i++ {
		pid, err := cont.Exec(&ExecRequest{
			Argv: []string{"/bin/true"},
			Env:  []string{"PATH=/bin"},
		})
		if err != nil {
			t.Fatalf("exec %d: %v", i, err)
		}
		code, err := cont.Wait(pid)
		if err != nil {
			t.Fatalf("wait %d: %v", i, err)
		}
		if code != 0 {
			t.Fatalf("exec %d: exit %d", i, code)
		}
	}
}

// =============================================================================
// Env propagation: overlay semantics + ResetEnv
// =============================================================================

// runAndCapture runs argv via env, captures stdout, and returns it on success.
func runAndCapture(t *testing.T, env ExecEnv, req *ExecRequest) string {
	t.Helper()
	r, w, _ := os.Pipe()
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()

	req.FDs = []*os.File{devNull, w, devNull}
	pid, err := env.Exec(req)
	w.Close()
	if err != nil {
		r.Close()
		t.Fatalf("exec: %v", err)
	}
	out, _ := io.ReadAll(r)
	r.Close()
	code, err := env.Wait(pid)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit %d, output: %s", code, string(out))
	}
	return string(out)
}

// TestBaseExecResetEnv verifies that ResetEnv:true on BaseExecEnv discards the
// host's os.Environ() base and replaces it with the minimal PATH-only env, then
// applies Env on top.
func TestBaseExecResetEnv(t *testing.T) {
	base := NewBaseExecEnv()
	defer base.Close()

	// Sentinel that should NOT leak through ResetEnv.
	t.Setenv("GL_TEST_LEAK_SENTINEL", "should_not_appear")

	out := runAndCapture(t, base, &ExecRequest{
		Argv:     []string{"/usr/bin/env"},
		Env:      []string{"FOO=bar42"},
		ResetEnv: true,
	})

	lines := strings.Split(strings.TrimSpace(out), "\n")
	gotKeys := make(map[string]string, len(lines))
	for _, line := range lines {
		if eq := strings.IndexByte(line, '='); eq >= 0 {
			gotKeys[line[:eq]] = line[eq+1:]
		}
	}

	if got, want := gotKeys["PATH"], ipc.DefaultPATH; got != want {
		t.Errorf("PATH = %q, want %q", got, want)
	}
	if got, want := gotKeys["FOO"], "bar42"; got != want {
		t.Errorf("FOO = %q, want %q", got, want)
	}
	if _, leaked := gotKeys["GL_TEST_LEAK_SENTINEL"]; leaked {
		t.Errorf("ResetEnv:true leaked host env: GL_TEST_LEAK_SENTINEL=%q", gotKeys["GL_TEST_LEAK_SENTINEL"])
	}
	if len(gotKeys) != 2 {
		t.Errorf("expected exactly 2 env entries (PATH, FOO), got %d: %v", len(gotKeys), gotKeys)
	}
}

// TestBaseExecOverlayInheritsHost verifies that with Env:nil, ResetEnv:false the
// child sees the host's environment unchanged, and that with a non-empty overlay
// the child sees host env + overlay merged.
func TestBaseExecOverlayInheritsHost(t *testing.T) {
	base := NewBaseExecEnv()
	defer base.Close()

	t.Setenv("GL_TEST_HOST_VAR", "host_value")

	// Case 1: nil overlay → child inherits host env wholesale.
	out := runAndCapture(t, base, &ExecRequest{
		Argv: []string{"/usr/bin/env"},
	})
	if !strings.Contains(out, "GL_TEST_HOST_VAR=host_value") {
		t.Errorf("expected host env to flow through; output:\n%s", out)
	}

	// Case 2: overlay adds a new key, host env still present.
	out = runAndCapture(t, base, &ExecRequest{
		Argv: []string{"/usr/bin/env"},
		Env:  []string{"FOO=overlay_added"},
	})
	if !strings.Contains(out, "GL_TEST_HOST_VAR=host_value") {
		t.Errorf("overlay dropped host env; output:\n%s", out)
	}
	if !strings.Contains(out, "FOO=overlay_added") {
		t.Errorf("overlay key missing; output:\n%s", out)
	}
}

// TestContainerInheritsStubResetEnv verifies that a child Exec'd inside a
// Container with no Env override sees PATH=ipc.DefaultPATH and nothing else from
// the host. The Container's stub was launched with ResetEnv:true (set in
// NewContainer), so its os.Environ() — which the child inherits — is the
// minimal PATH-only env.
func TestContainerInheritsStubResetEnv(t *testing.T) {
	stubPath := requireStub(t)
	rootfs := buildMinimalRootfs(t)

	t.Setenv("GL_TEST_LEAK_SENTINEL", "should_not_reach_container")

	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	// /usr/bin/env isn't in the minimal rootfs, but /bin/sh + builtin set is.
	cont, err := NewContainer(ContainerConfig{Ctx: testCtx(), Parent: userNS, StubPath: stubPath, Rootfs: rootfs})
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	defer cont.Close()

	out := runAndCapture(t, cont, &ExecRequest{
		Argv: []string{"/bin/sh", "-c", "set"},
	})

	// PATH must be the reset default.
	if !strings.Contains(out, "PATH='"+ipc.DefaultPATH+"'") && !strings.Contains(out, "PATH="+ipc.DefaultPATH) {
		t.Errorf("expected PATH=%s in container child env; output:\n%s", ipc.DefaultPATH, out)
	}
	// Host sentinel must not be present.
	if strings.Contains(out, "GL_TEST_LEAK_SENTINEL") {
		t.Errorf("ResetEnv:true on container stub leaked host env into child; output:\n%s", out)
	}
}

// =============================================================================
// FsContext: BaseFsContext (no stub) and RemoteExecEnv.Open (via MountNS)
// =============================================================================

func TestBaseFsContextOpenRead(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/hello.txt"
	if err := os.WriteFile(path, []byte("hello fscontext"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	fs := NewBaseFsContext()
	f, err := fs.Open(path, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "hello fscontext" {
		t.Fatalf("got %q, want %q", string(data), "hello fscontext")
	}
}

func TestBaseFsContextOpenWrite(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/out.txt"

	fs := NewBaseFsContext()
	f, err := fs.Open(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := f.Write([]byte("written by fscontext")); err != nil {
		f.Close()
		t.Fatalf("write: %v", err)
	}
	f.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "written by fscontext" {
		t.Fatalf("got %q, want %q", string(data), "written by fscontext")
	}
}

func TestMountNSOpen(t *testing.T) {
	stubPath := requireStub(t)
	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	mountNS, err := NewMountNS(MountNSConfig{Parent: userNS, StubPath: stubPath})
	if err != nil {
		t.Fatalf("create mountns: %v", err)
	}
	defer mountNS.Close()

	tmpDir := t.TempDir()
	if err := mountNS.Mount("tmpfs", tmpDir, "tmpfs", 0, "mode=0755"); err != nil {
		t.Fatalf("mount tmpfs: %v", err)
	}

	// Write content to a file via Exec+sh so it goes through the namespace.
	filePath := tmpDir + "/data.txt"
	if err := mountNS.CreateFile(filePath, 0644); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()
	r, w, _ := os.Pipe()
	pid, err := mountNS.Exec(&ExecRequest{
		Argv: []string{"/bin/sh", "-c", "echo -n 'namespace file content' > " + filePath},
		Env:  []string{"PATH=/bin:/usr/bin"},
		FDs:  []*os.File{devNull, w, devNull},
	})
	w.Close()
	r.Close()
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if code, err := mountNS.Wait(pid); err != nil || code != 0 {
		t.Fatalf("write via exec: code=%d err=%v", code, err)
	}

	// Open the file via MountNS.Open — gets FD back via SCM_RIGHTS from stub.
	f, err := mountNS.Open(filePath, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("MountNS.Open: %v", err)
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "namespace file content" {
		t.Fatalf("got %q, want %q", string(data), "namespace file content")
	}
}

func TestMountNSOpenWrite(t *testing.T) {
	stubPath := requireStub(t)
	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	mountNS, err := NewMountNS(MountNSConfig{Parent: userNS, StubPath: stubPath})
	if err != nil {
		t.Fatalf("create mountns: %v", err)
	}
	defer mountNS.Close()

	tmpDir := t.TempDir()
	if err := mountNS.Mount("tmpfs", tmpDir, "tmpfs", 0, "mode=0755"); err != nil {
		t.Fatalf("mount tmpfs: %v", err)
	}

	filePath := tmpDir + "/write.txt"

	// Open for writing via MountNS — stub opens the file in the namespace.
	f, err := mountNS.Open(filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatalf("MountNS.Open for write: %v", err)
	}
	if _, err := f.Write([]byte("written via open fd")); err != nil {
		f.Close()
		t.Fatalf("write to fd: %v", err)
	}
	f.Close()

	// Verify the content by reading via Exec+cat inside the namespace.
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()
	rr, ww, _ := os.Pipe()
	pid, err := mountNS.Exec(&ExecRequest{
		Argv: []string{"/bin/cat", filePath},
		Env:  []string{"PATH=/bin:/usr/bin"},
		FDs:  []*os.File{devNull, ww, devNull},
	})
	ww.Close()
	if err != nil {
		rr.Close()
		t.Fatalf("exec cat: %v", err)
	}
	out, _ := io.ReadAll(rr)
	rr.Close()
	if code, err := mountNS.Wait(pid); err != nil || code != 0 {
		t.Fatalf("cat: code=%d err=%v", code, err)
	}
	if string(out) != "written via open fd" {
		t.Fatalf("cat got %q, want %q", string(out), "written via open fd")
	}
}
