//go:build integration

package integration

import (
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_GracefulShutdown_SIGTERM compiles the binary, runs it against a
// real TCP backend, exercises a successful transfer, then sends SIGTERM
// while a client connection is still open. The process must exit with
// code 0 within shutdown_timeout — no hangs, no panics, no force-kill.
func TestE2E_GracefulShutdown_SIGTERM(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM semantics are not portable to Windows")
	}

	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "flowgate")

	moduleRoot := findModuleRoot(t)

	buildCmd := exec.Command("go", "build", "-o", binPath, "./cmd")
	buildCmd.Dir = moduleRoot
	buildCmd.Env = os.Environ()
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	backend := startTCPEcho(t, 0)
	proxyAddr := mustFreePort(t)

	cfgPath := filepath.Join(binDir, "config.yaml")
	cfgYAML := "env: test\n" +
		"log_level: warn\n" +
		"server:\n" +
		"  instance_id: e2e-shutdown\n" +
		"  shutdown_timeout: 5s\n" +
		"defaults:\n" +
		"  connect_timeout: 1s\n" +
		"  idle_timeout: 5s\n" +
		"  max_conns: 64\n" +
		"  buf_size: 32768\n" +
		"routes:\n" +
		"  - name: r1\n" +
		"    protocol: tcp\n" +
		"    listen: \"" + proxyAddr + "\"\n" +
		"    balancer: round_robin\n" +
		"    backends:\n" +
		"      - addr: \"" + backend.addr + "\"\n" +
		"        weight: 1\n"
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfgYAML), 0o600))

	envPath := filepath.Join(binDir, ".env")
	require.NoError(t, os.WriteFile(envPath, []byte("CONFIG_PATH="+cfgPath+"\n"), 0o600))

	cmd := exec.Command(binPath)
	cmd.Dir = binDir
	cmd.Env = append(os.Environ(), "CONFIG_PATH="+cfgPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stderr, err := cmd.StderrPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)

	require.NoError(t, cmd.Start())

	var (
		logMu  sync.Mutex
		logBuf []byte
	)
	collect := func(r io.Reader) {
		buf := make([]byte, 4096)
		for {
			n, rerr := r.Read(buf)
			if n > 0 {
				logMu.Lock()
				logBuf = append(logBuf, buf[:n]...)
				logMu.Unlock()
			}
			if rerr != nil {
				return
			}
		}
	}
	go collect(stderr)
	go collect(stdout)

	t.Cleanup(func() {
		if cmd.Process == nil {
			return
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	})

	require.Eventually(t, func() bool {
		c, derr := net.DialTimeout("tcp", proxyAddr, 100*time.Millisecond)
		if derr != nil {
			return false
		}
		_ = c.Close()
		return true
	}, 5*time.Second, 50*time.Millisecond, "proxy never opened listening socket")

	// Step 1: validate data plane is alive — a complete round-trip succeeds.
	{
		c, err := net.DialTimeout("tcp", proxyAddr, 1*time.Second)
		require.NoError(t, err)
		require.NoError(t, c.SetDeadline(time.Now().Add(3*time.Second)))
		payload := []byte("preflight")
		_, err = c.Write(payload)
		require.NoError(t, err)
		if cw, ok := c.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		got, err := io.ReadAll(c)
		require.NoError(t, err)
		require.Equal(t, payload, got, "data plane must work before shutdown")
		_ = c.Close()
	}

	// Step 2: open another connection and keep it alive (active transfer state),
	// then deliver SIGTERM.
	active, err := net.DialTimeout("tcp", proxyAddr, 1*time.Second)
	require.NoError(t, err)
	defer func() { _ = active.Close() }()
	require.NoError(t, active.SetDeadline(time.Now().Add(8*time.Second)))
	_, err = active.Write([]byte("midflight"))
	require.NoError(t, err)

	time.Sleep(50 * time.Millisecond)
	require.NoError(t, syscall.Kill(cmd.Process.Pid, syscall.SIGTERM))

	// Step 3: the process must exit cleanly with code 0 within
	// shutdown_timeout (5s) + grace.
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	select {
	case werr := <-waitDone:
		assert.NoError(t, werr, "process must exit cleanly (exit code 0) after SIGTERM")
	case <-time.After(10 * time.Second):
		logMu.Lock()
		t.Logf("captured output:\n%s", string(logBuf))
		logMu.Unlock()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		t.Fatal("process did not exit within 10s of SIGTERM")
	}

	// Step 4: post-shutdown, the listener must be gone — new dials fail.
	_, dialErr := net.DialTimeout("tcp", proxyAddr, 200*time.Millisecond)
	assert.Error(t, dialErr, "listener must be closed after shutdown")
}

func findModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate go.mod walking up from cwd")
		}
		dir = parent
	}
}

func mustFreePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}
